package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Entry is a stored memory in the swarm knowledge base.
type Entry struct {
	ID       string   `json:"id"`
	AgentID  string   `json:"agent_id"`
	Content  string   `json:"content"`
	Topics   []string `json:"topics"`
	StoredAt string   `json:"stored_at"`
}

// Store provides shared swarm memory backed by Redis.
// When a VectorClient and Embedder are configured (via WithVector), Recall
// performs semantic search and merges results with keyword matches.
// All vector operations are best-effort: failures fall back to keyword search
// without surfacing errors to the caller.
type Store struct {
	rdb          *redis.Client
	ns           string
	vectorClient VectorClient // nil = keyword-only
	embedder     Embedder     // nil = keyword-only
}

// New creates a memory store connected to Redis.
func New(redisURL, namespace string) (*Store, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	rdb := redis.NewClient(opts)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return &Store{rdb: rdb, ns: namespace}, nil
}

// WithVector returns a copy of the Store with semantic search enabled.
// Both vc and emb must be non-nil; if either is nil the store behaves as
// keyword-only.  The underlying Redis connection is shared.
func (s *Store) WithVector(vc VectorClient, emb Embedder) *Store {
	return &Store{
		rdb:          s.rdb,
		ns:           s.ns,
		vectorClient: vc,
		embedder:     emb,
	}
}

// Put stores a memory entry. Returns the entry ID.
// When a VectorClient and Embedder are configured the entry is also indexed
// for semantic search; embedding failures are silently ignored so the Redis
// write always succeeds.
func (s *Store) Put(ctx context.Context, agentID, content string, topics []string) (string, error) {
	id := fmt.Sprintf("%d-%s", time.Now().UnixMilli(), randomSuffix())
	entry := Entry{
		ID:       id,
		AgentID:  agentID,
		Content:  content,
		Topics:   topics,
		StoredAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}

	pipe := s.rdb.Pipeline()
	pipe.ZAdd(ctx, s.key("memories"), redis.Z{Score: float64(time.Now().UnixMilli()), Member: data})
	for _, topic := range topics {
		pipe.SAdd(ctx, s.key("topic:"+topic), id)
	}
	pipe.Set(ctx, s.key("memory:"+id), data, 30*24*time.Hour)
	_, err = pipe.Exec(ctx)
	if err != nil {
		return "", err
	}

	// Best-effort: index embedding for semantic search.
	if s.vectorClient != nil && s.embedder != nil {
		text := content + " " + strings.Join(topics, " ")
		if vec, embErr := s.embedder.Embed(ctx, text); embErr != nil {
			log.Printf("WARN memory.Put: embed failed (collection=%s id=%s agent=%s): %v", s.collectionName(), id, agentID, embErr)
		} else {
			payload := map[string]interface{}{
				"entry_id": id,
				"agent_id": agentID,
				"content":  content,
				"topics":   strings.Join(topics, " "),
			}
			if upErr := s.vectorClient.Upsert(ctx, s.collectionName(), id, vec, payload); upErr != nil {
				log.Printf("WARN memory.Put: qdrant upsert failed (collection=%s id=%s): %v", s.collectionName(), id, upErr)
			}
		}
	}

	return id, nil
}

// Recall searches memories by keyword matching, augmented by semantic vector
// search when a VectorClient and Embedder are configured.
//
// With vector search: Qdrant nearest-neighbour results come first (ordered by
// cosine similarity), followed by any keyword-only matches not already in the
// vector result set.  Failures in the vector path fall back to keyword-only.
//
// Without vector search: behaves exactly as before — O(N) keyword scan over
// the most recent 200 entries.
func (s *Store) Recall(ctx context.Context, query string, limit int) ([]Entry, error) {
	kwResults, err := s.recallByKeyword(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	if s.vectorClient == nil || s.embedder == nil {
		return kwResults, nil
	}

	vec, err := s.embedder.Embed(ctx, query)
	if err != nil {
		log.Printf("WARN memory.Recall: embed failed (collection=%s): %v — falling back to keyword", s.collectionName(), err)
		return kwResults, nil // fallback: embedding unavailable
	}

	vecHits, err := s.vectorClient.Search(ctx, s.collectionName(), vec, limit)
	if err != nil {
		log.Printf("WARN memory.Recall: qdrant search failed (collection=%s): %v — falling back to keyword", s.collectionName(), err)
		return kwResults, nil // fallback: vector DB unavailable
	}

	// Merge: vector results (highest similarity first), then keyword-only extras.
	seen := make(map[string]bool, len(vecHits)+len(kwResults))
	merged := make([]Entry, 0, limit)

	for _, hit := range vecHits {
		if seen[hit.ID] || len(merged) >= limit {
			continue
		}
		e, fetchErr := s.entryByID(ctx, hit.ID)
		if fetchErr != nil {
			continue
		}
		seen[hit.ID] = true
		merged = append(merged, e)
	}

	for _, e := range kwResults {
		if !seen[e.ID] && len(merged) < limit {
			seen[e.ID] = true
			merged = append(merged, e)
		}
	}

	return merged, nil
}

// WithSquad returns a Store that scopes all keys under <ns>:<squadNS>:.
// The underlying Redis connection and any vector config are shared.
// Returns s unchanged when squadNS is empty.
func (s *Store) WithSquad(squadNS string) *Store {
	if squadNS == "" {
		return s
	}
	return &Store{
		rdb:          s.rdb,
		ns:           s.ns + ":" + squadNS,
		vectorClient: s.vectorClient,
		embedder:     s.embedder,
	}
}

// RegisterSquad adds squadNS to the set of known squad namespaces on the
// root store, so that RecallCrossSquad can discover it later.
func (s *Store) RegisterSquad(ctx context.Context, squadNS string) error {
	return s.rdb.SAdd(ctx, s.key("squads"), squadNS).Err()
}

// SquadNames returns every squad namespace that has been registered via
// RegisterSquad on this store.
func (s *Store) SquadNames(ctx context.Context) ([]string, error) {
	return s.rdb.SMembers(ctx, s.key("squads")).Result()
}

// RecallCrossSquad searches memories in the root namespace plus every
// registered squad namespace, deduplicating by entry ID.
func (s *Store) RecallCrossSquad(ctx context.Context, query string, limit int) ([]Entry, error) {
	squads, _ := s.SquadNames(ctx)

	seen := make(map[string]bool)
	var results []Entry

	search := func(st *Store) {
		if len(results) >= limit {
			return
		}
		entries, err := st.Recall(ctx, query, limit)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !seen[e.ID] && len(results) < limit {
				seen[e.ID] = true
				results = append(results, e)
			}
		}
	}

	search(s) // root namespace first
	for _, name := range squads {
		search(s.WithSquad(name))
	}
	return results, nil
}

// Close shuts down the Redis connection.
func (s *Store) Close() error {
	return s.rdb.Close()
}

// recallByKeyword is the keyword-scan fallback: scans the most recent 200
// entries in the sorted set and returns those containing any query keyword.
func (s *Store) recallByKeyword(ctx context.Context, query string, limit int) ([]Entry, error) {
	raw, err := s.rdb.ZRevRange(ctx, s.key("memories"), 0, 200).Result()
	if err != nil {
		return nil, err
	}

	keywords := strings.Fields(strings.ToLower(query))
	var matches []Entry
	for _, r := range raw {
		var e Entry
		if err := json.Unmarshal([]byte(r), &e); err != nil {
			continue
		}
		text := strings.ToLower(e.Content + " " + strings.Join(e.Topics, " "))
		for _, kw := range keywords {
			if strings.Contains(text, kw) {
				matches = append(matches, e)
				break
			}
		}
		if len(matches) >= limit {
			break
		}
	}
	return matches, nil
}

// entryByID fetches a single Entry from Redis by its ID.
func (s *Store) entryByID(ctx context.Context, id string) (Entry, error) {
	data, err := s.rdb.Get(ctx, s.key("memory:"+id)).Result()
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	if err := json.Unmarshal([]byte(data), &e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// collectionName converts the namespace into a Qdrant-safe collection name
// (colons and hyphens replaced with underscores).
func (s *Store) collectionName() string {
	r := strings.NewReplacer(":", "_", "-", "_")
	return r.Replace(s.ns)
}

func (s *Store) key(suffix string) string {
	return s.ns + ":" + suffix
}

func randomSuffix() string {
	return fmt.Sprintf("%06x", time.Now().UnixNano()&0xFFFFFF)
}
