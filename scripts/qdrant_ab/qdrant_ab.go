// qdrant_ab.go — Keyword vs Semantic retrieval A/B harness for issue #215.
//
// Goal: settle empirically whether Voyage+cosine semantic search beats the
// existing substring keyword scan over the ~582 Redis memory entries.
//
// Decision gate (from #215):
//   MRR@3(semantic) - MRR@3(keyword) >= 0.10  → KEEP Qdrant + Voyage
//                                        < 0.10  → RIP, keyword is enough
//
// The harness:
//   1. Loads all memory entries from Redis (key: <ns>:memories).
//   2. Mirrors Store.recallByKeyword (substring/OR match, lowercase, top-200).
//   3. Embeds every entry via Voyage /v1/embeddings (cached on disk).
//   4. For each gold query: runs both paths, computes reciprocal rank of the
//      first expected_id appearing in top-3. MRR = mean over queries with a
//      non-empty gold set.
//   5. Prints a single-table summary + per-query rows.
//
// Intentionally has NO Qdrant dependency — pure in-memory cosine over
// []float32 vectors so it runs when Qdrant is down.
//
// Env:
//   OCTI_REDIS_URL      (default: redis://localhost:6379/0)
//   OCTI_MEMORY_NS      (default: octi)
//   VOYAGE_API_KEY      (required for --with-semantic; exit 2 if missing)
//   OCTI_EMBEDDINGS_URL (default: https://api.voyageai.com)
//   OCTI_EMBEDDINGS_MODEL (default: voyage-3-lite)
//
// Flags:
//   --queries FILE      gold set yaml (default: scripts/qdrant_ab/queries.yaml)
//   --dump-sample       print 10 random memory entries (for gold-set seeding)
//                       and exit. No embedding calls.
//   --limit N           top-N for ranking (default 3, matches MRR@3)
//   --cache FILE        embedding cache path (default: scripts/qdrant_ab/embed_cache.json)
//
// Run:
//   cd octi
//   go run ./scripts/qdrant_ab
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/memory"
	"github.com/redis/go-redis/v9"
	"gopkg.in/yaml.v3"
)

type goldSet struct {
	Queries []struct {
		Query       string   `yaml:"query"`
		ExpectedIDs []string `yaml:"expected_ids"`
	} `yaml:"queries"`
}

type scoredEntry struct {
	entry memory.Entry
	score float32
}

func main() {
	var (
		queriesPath = flag.String("queries", "scripts/qdrant_ab/queries.yaml", "gold-set YAML")
		dumpSample  = flag.Bool("dump-sample", false, "dump 10 sample entries and exit")
		limit       = flag.Int("limit", 3, "top-N for MRR@N")
		cachePath   = flag.String("cache", "scripts/qdrant_ab/embed_cache.json", "embedding cache path")
	)
	flag.Parse()

	redisURL := envOr("OCTI_REDIS_URL", "redis://localhost:6379/0")
	ns := envOr("OCTI_MEMORY_NS", "octi")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		die("parse redis url: %v", err)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		die("redis ping: %v", err)
	}

	entries, err := loadAllEntries(ctx, rdb, ns)
	if err != nil {
		die("load entries: %v", err)
	}
	fmt.Fprintf(os.Stderr, "loaded %d entries from %s:memories\n", len(entries), ns)

	if *dumpSample {
		dump := entries
		if len(dump) > 10 {
			dump = dump[:10]
		}
		for _, e := range dump {
			content := e.Content
			if len(content) > 120 {
				content = content[:120] + "…"
			}
			fmt.Printf("id=%s  topics=%v\n  %s\n", e.ID, e.Topics, content)
		}
		return
	}

	// Load gold set.
	var gold goldSet
	raw, err := os.ReadFile(*queriesPath)
	if err != nil {
		die("read queries: %v", err)
	}
	if err := yaml.Unmarshal(raw, &gold); err != nil {
		die("parse queries: %v", err)
	}
	fmt.Fprintf(os.Stderr, "loaded %d queries (%d with gold ids)\n",
		len(gold.Queries), countLabeled(gold))

	// ─── Semantic setup ──────────────────────────────────────────────────────
	voyageKey := os.Getenv("VOYAGE_API_KEY")
	if voyageKey == "" {
		voyageKey = os.Getenv("OCTI_EMBEDDINGS_KEY")
	}
	if voyageKey == "" {
		fmt.Fprintln(os.Stderr, "ERROR: VOYAGE_API_KEY (or OCTI_EMBEDDINGS_KEY) not set — cannot run semantic path.")
		fmt.Fprintln(os.Stderr, "       Set it, or re-run with keyword-only analysis on a different harness.")
		os.Exit(2)
	}
	embURL := envOr("OCTI_EMBEDDINGS_URL", "https://api.voyageai.com")
	embModel := envOr("OCTI_EMBEDDINGS_MODEL", "voyage-3-lite")
	emb := memory.NewHTTPEmbedder(embURL, voyageKey, embModel)

	cache := loadCache(*cachePath)
	defer saveCache(*cachePath, cache)

	// Embed corpus (cached).
	fmt.Fprintf(os.Stderr, "embedding corpus (%d entries, cached where possible)…\n", len(entries))
	corpusVecs := make(map[string][]float32, len(entries))
	misses := 0
	for i, e := range entries {
		text := e.Content + " " + strings.Join(e.Topics, " ")
		key := "doc:" + e.ID
		if v, ok := cache[key]; ok {
			corpusVecs[e.ID] = v
			continue
		}
		v, err := emb.Embed(ctx, text)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  embed fail id=%s: %v (skipping)\n", e.ID, err)
			continue
		}
		cache[key] = v
		corpusVecs[e.ID] = v
		misses++
		if misses%25 == 0 {
			fmt.Fprintf(os.Stderr, "  embedded %d/%d (live calls: %d)\n", i+1, len(entries), misses)
			saveCache(*cachePath, cache)
		}
	}
	fmt.Fprintf(os.Stderr, "corpus embed done: %d vectors, %d live calls\n", len(corpusVecs), misses)

	// ─── Run both paths ──────────────────────────────────────────────────────
	type row struct {
		Q        string
		KwRR     float64
		SemRR    float64
		KwTop    []string
		SemTop   []string
		Labeled  bool
	}
	var rows []row
	var kwSum, semSum float64
	labeled := 0

	for _, q := range gold.Queries {
		kwHits := keywordTopN(q.Query, entries, *limit)
		kwIDs := idsOf(kwHits)

		qText := q.Query
		qKey := "q:" + qText
		var qVec []float32
		if v, ok := cache[qKey]; ok {
			qVec = v
		} else {
			v, err := emb.Embed(ctx, qText)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  query embed fail %q: %v\n", qText, err)
				continue
			}
			cache[qKey] = v
			qVec = v
		}
		semHits := semanticTopN(qVec, entries, corpusVecs, *limit)
		semIDs := idsOf(semHits)

		r := row{Q: qText, KwTop: kwIDs, SemTop: semIDs}
		if len(q.ExpectedIDs) > 0 {
			r.Labeled = true
			r.KwRR = reciprocalRank(kwIDs, q.ExpectedIDs)
			r.SemRR = reciprocalRank(semIDs, q.ExpectedIDs)
			kwSum += r.KwRR
			semSum += r.SemRR
			labeled++
		}
		rows = append(rows, r)
	}

	// ─── Report ──────────────────────────────────────────────────────────────
	fmt.Println()
	fmt.Println("================================================================")
	fmt.Printf("  Qdrant A/B Harness — issue #215 — MRR@%d\n", *limit)
	fmt.Println("================================================================")
	fmt.Printf("corpus:   %d entries (ns=%s)\n", len(entries), ns)
	fmt.Printf("queries:  %d total, %d labeled\n", len(gold.Queries), labeled)
	fmt.Printf("embed:    %s / %s\n", embURL, embModel)
	fmt.Println()

	if labeled == 0 {
		fmt.Println("NO GOLD SET — MRR skipped. Top-3 samples below; use --dump-sample")
		fmt.Println("to pick expected_ids and populate queries.yaml.")
		fmt.Println()
		for i, r := range rows {
			if i >= 8 {
				fmt.Printf("… (%d more)\n", len(rows)-i)
				break
			}
			fmt.Printf("Q: %q\n", r.Q)
			fmt.Printf("  keyword top-%d:  %v\n", *limit, r.KwTop)
			fmt.Printf("  semantic top-%d: %v\n", *limit, r.SemTop)
		}
		return
	}

	kwMRR := kwSum / float64(labeled)
	semMRR := semSum / float64(labeled)
	delta := semMRR - kwMRR

	fmt.Printf("%-40s %10s %10s %10s\n", "path", "MRR@3", "delta", "decision")
	fmt.Println(strings.Repeat("-", 72))
	fmt.Printf("%-40s %10.4f %10s %10s\n", "keyword (substring OR match)", kwMRR, "—", "baseline")
	verdict := "KEEP (>=0.10)"
	if delta < 0.10 {
		verdict = "RIP (<0.10)"
	}
	fmt.Printf("%-40s %10.4f %+10.4f %10s\n", "semantic (voyage + cosine)", semMRR, delta, verdict)
	fmt.Println()
	fmt.Println("per-query (labeled only):")
	for _, r := range rows {
		if !r.Labeled {
			continue
		}
		fmt.Printf("  kw=%.2f sem=%.2f  %q\n", r.KwRR, r.SemRR, r.Q)
	}
}

// ─── retrieval paths ────────────────────────────────────────────────────────

// keywordTopN mirrors Store.recallByKeyword: substring-OR on lowercased text.
// Order preserved = ZRevRange order (most recent first) among matches.
func keywordTopN(query string, entries []memory.Entry, n int) []memory.Entry {
	keywords := strings.Fields(strings.ToLower(query))
	if len(keywords) == 0 {
		return nil
	}
	var matches []memory.Entry
	for _, e := range entries {
		text := strings.ToLower(e.Content + " " + strings.Join(e.Topics, " "))
		for _, kw := range keywords {
			if strings.Contains(text, kw) {
				matches = append(matches, e)
				break
			}
		}
		if len(matches) >= n {
			break
		}
	}
	return matches
}

func semanticTopN(qVec []float32, entries []memory.Entry, vecs map[string][]float32, n int) []memory.Entry {
	scored := make([]scoredEntry, 0, len(entries))
	for _, e := range entries {
		v, ok := vecs[e.ID]
		if !ok {
			continue
		}
		scored = append(scored, scoredEntry{entry: e, score: cosine(qVec, v)})
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	if len(scored) > n {
		scored = scored[:n]
	}
	out := make([]memory.Entry, 0, len(scored))
	for _, s := range scored {
		out = append(out, s.entry)
	}
	return out
}

func cosine(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

// ─── metrics ────────────────────────────────────────────────────────────────

func reciprocalRank(got, want []string) float64 {
	wantSet := map[string]bool{}
	for _, w := range want {
		wantSet[w] = true
	}
	for i, g := range got {
		if wantSet[g] {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}

// ─── data loading ───────────────────────────────────────────────────────────

func loadAllEntries(ctx context.Context, rdb *redis.Client, ns string) ([]memory.Entry, error) {
	raw, err := rdb.ZRevRange(ctx, ns+":memories", 0, -1).Result()
	if err != nil {
		return nil, err
	}
	out := make([]memory.Entry, 0, len(raw))
	for _, r := range raw {
		var e memory.Entry
		if err := json.Unmarshal([]byte(r), &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// ─── embedding cache ────────────────────────────────────────────────────────

func loadCache(path string) map[string][]float32 {
	m := map[string][]float32{}
	b, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	_ = json.Unmarshal(b, &m)
	return m
}

func saveCache(path string, m map[string][]float32) {
	_ = os.MkdirAll(dirOf(path), 0o755)
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, b, 0o644)
}

func dirOf(p string) string {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "."
	}
	return p[:i]
}

// ─── helpers ────────────────────────────────────────────────────────────────

func idsOf(es []memory.Entry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func countLabeled(g goldSet) int {
	n := 0
	for _, q := range g.Queries {
		if len(q.ExpectedIDs) > 0 {
			n++
		}
	}
	return n
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
	os.Exit(1)
}
