package memory

import (
	"context"
	"errors"
	"math"
	"sort"
	"testing"
)

// ─── Mock implementations ─────────────────────────────────────────────────────

// mockVectorClient is an in-memory VectorClient for tests.
type mockVectorClient struct {
	points     map[string]mockPoint // entry_id → point
	upsertErr  error                // if non-nil, Upsert returns this error
	searchErr  error                // if non-nil, Search returns this error
}

type mockPoint struct {
	vector  []float32
	payload map[string]interface{}
}

func newMockVectorClient() *mockVectorClient {
	return &mockVectorClient{points: make(map[string]mockPoint)}
}

func (m *mockVectorClient) Upsert(_ context.Context, _, id string, vector []float32, payload map[string]interface{}) error {
	if m.upsertErr != nil {
		return m.upsertErr
	}
	m.points[id] = mockPoint{vector: vector, payload: payload}
	return nil
}

func (m *mockVectorClient) Search(_ context.Context, _ string, query []float32, limit int) ([]SearchResult, error) {
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	results := make([]SearchResult, 0, len(m.points))
	for _, p := range m.points {
		entryID, _ := p.payload["entry_id"].(string)
		if entryID == "" {
			continue
		}
		results = append(results, SearchResult{
			ID:    entryID,
			Score: cosineSim(query, p.vector),
		})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// mockEmbedder produces deterministic 8-dimensional vectors from text content.
// Characters that appear in the text contribute to specific dimensions, so
// texts with similar character distributions get higher cosine similarity.
type mockEmbedder struct {
	embedErr error // if non-nil, Embed returns this error
}

func (m *mockEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	if m.embedErr != nil {
		return nil, m.embedErr
	}
	const dim = 8
	vec := make([]float32, dim)
	for i, ch := range text {
		vec[i%dim] += float32(ch)
	}
	normalize(vec)
	return vec, nil
}

// ─── Math helpers ─────────────────────────────────────────────────────────────

func cosineSim(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float32
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrt32(na) * sqrt32(nb))
}

func normalize(v []float32) {
	var sum float32
	for _, x := range v {
		sum += x * x
	}
	if sum == 0 {
		return
	}
	s := float32(1 / math.Sqrt(float64(sum)))
	for i := range v {
		v[i] *= s
	}
}

func sqrt32(x float32) float32 {
	return float32(math.Sqrt(float64(x)))
}

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestWithVector_PutIndexesEmbedding(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	vc := newMockVectorClient()
	emb := &mockEmbedder{}
	vs := store.WithVector(vc, emb)

	id, err := vs.Put(ctx, "agent-a", "postgres migration failed on prod", []string{"database", "incident"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if _, ok := vc.points[id]; !ok {
		t.Errorf("expected embedding to be upserted for entry %q, but mock vector client has no entry", id)
	}
}

func TestWithVector_PutEmbedErrorIsNonFatal(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	vc := newMockVectorClient()
	emb := &mockEmbedder{embedErr: errors.New("embedding service unavailable")}
	vs := store.WithVector(vc, emb)

	// Put should succeed even when the embedder fails.
	id, err := vs.Put(ctx, "agent-a", "some content", []string{"test"})
	if err != nil {
		t.Fatalf("Put should succeed despite embed error, got: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty entry ID")
	}
	// No embedding should have been stored.
	if len(vc.points) != 0 {
		t.Errorf("expected no vector points after embed error, got %d", len(vc.points))
	}
}

func TestWithVector_RecallVectorResultsFirst(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	vc := newMockVectorClient()
	emb := &mockEmbedder{}
	vs := store.WithVector(vc, emb)

	// Store two entries: one keyword-matching, one vector-matching.
	_, err := vs.Put(ctx, "agent-a", "postgres migration failed on prod", []string{"database", "incident"})
	if err != nil {
		t.Fatalf("Put database entry: %v", err)
	}
	_, err = vs.Put(ctx, "agent-b", "frontend CSS refactor completed", []string{"frontend", "ui"})
	if err != nil {
		t.Fatalf("Put frontend entry: %v", err)
	}

	// Query a keyword that matches neither, but vector matches the first.
	results, err := vs.Recall(ctx, "postgres migration failed on prod", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	if results[0].Content != "postgres migration failed on prod" {
		t.Errorf("expected database entry first, got %q", results[0].Content)
	}
}

func TestWithVector_RecallFallsBackOnEmbedError(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	vc := newMockVectorClient()
	emb := &mockEmbedder{}
	vs := store.WithVector(vc, emb)

	_, err := vs.Put(ctx, "agent-a", "redis connection pool exhausted", []string{"redis", "ops"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Simulate embedder failure at query time.
	vs2 := store.WithVector(vc, &mockEmbedder{embedErr: errors.New("embed fail")})
	results, err := vs2.Recall(ctx, "redis connection", 5)
	if err != nil {
		t.Fatalf("Recall should not propagate embed error: %v", err)
	}
	// Keyword fallback should still find the entry.
	if len(results) == 0 {
		t.Error("expected keyword fallback to return results when embed fails")
	}
}

func TestWithVector_RecallFallsBackOnSearchError(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	vc := newMockVectorClient()
	emb := &mockEmbedder{}
	vs := store.WithVector(vc, emb)

	_, err := vs.Put(ctx, "agent-a", "circuit breaker opened for codex driver", []string{"circuit-breaker"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Simulate vector search failure at query time.
	failVC := &mockVectorClient{
		points:    vc.points,
		searchErr: errors.New("qdrant unreachable"),
	}
	vs2 := store.WithVector(failVC, emb)
	results, err := vs2.Recall(ctx, "circuit breaker", 5)
	if err != nil {
		t.Fatalf("Recall should not propagate search error: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected keyword fallback to return results when vector search fails")
	}
}

func TestWithVector_NilClientOrEmbedderIsKeywordOnly(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	// nil embedder → keyword-only
	vs := store.WithVector(newMockVectorClient(), nil)
	_, err := vs.Put(ctx, "agent-a", "memory without embedder", []string{"test"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	results, err := vs.Recall(ctx, "memory without", 5)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected keyword match")
	}
}

func TestWithVector_PreservesSquadScope(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	vc := newMockVectorClient()
	emb := &mockEmbedder{}
	vs := store.WithVector(vc, emb)

	// Squad-scoped store should inherit the vector config.
	squadStore := vs.WithSquad("alpha")
	_, err := squadStore.Put(ctx, "agent-a", "squad alpha knowledge", []string{"alpha"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// The embedding should have been indexed under the squad's collection name.
	if len(vc.points) == 0 {
		t.Error("expected squad-scoped Put to index an embedding via inherited vector config")
	}
}

func TestWithVector_RecallDeduplicates(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	vc := newMockVectorClient()
	emb := &mockEmbedder{}
	vs := store.WithVector(vc, emb)

	_, err := vs.Put(ctx, "agent-a", "unique dedup target entry", []string{"dedup"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	results, err := vs.Recall(ctx, "unique dedup target entry", 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	// Count by ID to verify no duplicates.
	seen := make(map[string]int)
	for _, r := range results {
		seen[r.ID]++
	}
	for id, count := range seen {
		if count > 1 {
			t.Errorf("entry %q appears %d times in results (expected 1)", id, count)
		}
	}
}

func TestCollectionName_SanitizesNamespace(t *testing.T) {
	cases := []struct {
		ns   string
		want string
	}{
		{"octi", "octi"},
		{"octi:pulpo", "octi_pulpo"},
		{"octi:pulpo:squad-a", "octi_pulpo_squad_a"},
		{"my-namespace", "my_namespace"},
	}
	for _, tc := range cases {
		s := &Store{ns: tc.ns}
		if got := s.collectionName(); got != tc.want {
			t.Errorf("collectionName(%q) = %q, want %q", tc.ns, got, tc.want)
		}
	}
}
