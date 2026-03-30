package standup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Report is a squad's async standup entry for a given date.
type Report struct {
	Squad     string   `json:"squad"`
	Done      []string `json:"done"`
	Doing     []string `json:"doing"`
	Blocked   []string `json:"blocked"`
	Requests  []string `json:"requests"`
	AuthorID  string   `json:"author_id"`
	Timestamp string   `json:"timestamp"`
}

// Status summarises a squad's standup health for display.
type Status string

const (
	StatusGreen  Status = "GREEN"
	StatusYellow Status = "YELLOW"
	StatusRed    Status = "RED"
)

// StatusOf derives a traffic-light status from a report:
//   - RED    — nothing done and has blockers, or completely empty
//   - YELLOW — has blockers but also has done items
//   - GREEN  — no blockers
func StatusOf(r *Report) Status {
	hasDone := len(r.Done) > 0
	hasBlocked := len(r.Blocked) > 0
	if !hasDone && hasBlocked {
		return StatusRed
	}
	if !hasDone && len(r.Doing) == 0 {
		return StatusRed
	}
	if hasBlocked {
		return StatusYellow
	}
	return StatusGreen
}

// Store manages standup entries in Redis.
type Store struct {
	rdb *redis.Client
	ns  string
}

// New connects to Redis and returns a Store.
func New(rdb *redis.Client, namespace string) *Store {
	return &Store{rdb: rdb, ns: namespace}
}

// Put stores or overwrites today's standup for squad.
// Returns the key under which it was stored.
func (s *Store) Put(ctx context.Context, squad, authorID string, done, doing, blocked, requests []string) (*Report, error) {
	r := &Report{
		Squad:     squad,
		Done:      done,
		Doing:     doing,
		Blocked:   blocked,
		Requests:  requests,
		AuthorID:  authorID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}

	date := time.Now().UTC().Format("2006-01-02")
	key := s.key(squad, date)

	// Keep standup data for 7 days.
	if err := s.rdb.Set(ctx, key, data, 7*24*time.Hour).Err(); err != nil {
		return nil, fmt.Errorf("store standup for %s: %w", squad, err)
	}

	// Track which squads reported today so GetAllToday can discover them.
	setKey := s.ns + ":standups:squads:" + date
	s.rdb.SAdd(ctx, setKey, squad)
	s.rdb.Expire(ctx, setKey, 7*24*time.Hour)

	return r, nil
}

// Get retrieves the standup report for squad on date (YYYY-MM-DD).
// Returns nil, nil when no report exists.
func (s *Store) Get(ctx context.Context, squad, date string) (*Report, error) {
	raw, err := s.rdb.Get(ctx, s.key(squad, date)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get standup %s/%s: %w", squad, date, err)
	}
	var r Report
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// GetToday returns squad's standup for today's UTC date.
func (s *Store) GetToday(ctx context.Context, squad string) (*Report, error) {
	return s.Get(ctx, squad, time.Now().UTC().Format("2006-01-02"))
}

// GetAllToday returns all standup reports filed today, keyed by squad name.
func (s *Store) GetAllToday(ctx context.Context) (map[string]*Report, error) {
	date := time.Now().UTC().Format("2006-01-02")
	setKey := s.ns + ":standups:squads:" + date

	squads, err := s.rdb.SMembers(ctx, setKey).Result()
	if err != nil {
		return nil, fmt.Errorf("list squads: %w", err)
	}

	results := make(map[string]*Report, len(squads))
	for _, sq := range squads {
		r, err := s.Get(ctx, sq, date)
		if err != nil || r == nil {
			continue
		}
		results[sq] = r
	}
	return results, nil
}

func (s *Store) key(squad, date string) string {
	return fmt.Sprintf("%s:standup:%s:%s", s.ns, squad, date)
}
