package standup

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/redis/go-redis/v9"
)

// Report is a squad's async standup for one day.
type Report struct {
	Squad     string    `json:"squad"`
	Date      string    `json:"date"`       // YYYY-MM-DD
	Done      []string  `json:"done"`       // completed since last standup
	Doing     []string  `json:"doing"`      // in-progress
	Blocked   []string  `json:"blocked"`    // blockers (empty = none)
	Requests  []string  `json:"requests"`   // cross-squad asks
	PostedAt  time.Time `json:"posted_at"`
	PostedBy  string    `json:"posted_by"`  // agent ID
}

// Store is a Redis-backed standup store.
type Store struct {
	rdb *redis.Client
	ns  string
}

// New creates a standup store. redisURL must be parseable by go-redis.
func New(rdb *redis.Client, namespace string) *Store {
	return &Store{rdb: rdb, ns: namespace}
}

// Post saves a standup report. If a report already exists for the same squad
// and date, it is overwritten — EMs can re-post to amend.
func (s *Store) Post(ctx context.Context, r Report) error {
	if r.Date == "" {
		r.Date = today()
	}
	if r.PostedAt.IsZero() {
		r.PostedAt = time.Now().UTC()
	}

	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal standup: %w", err)
	}

	pipe := s.rdb.Pipeline()
	// Store the report keyed by squad+date (overwrites previous posts for today)
	pipe.Set(ctx, s.key(r.Squad, r.Date), data, 14*24*time.Hour)
	// Track which squads have posted for this date
	pipe.SAdd(ctx, s.dateKey(r.Date), r.Squad)
	pipe.Expire(ctx, s.dateKey(r.Date), 14*24*time.Hour)
	_, err = pipe.Exec(ctx)
	return err
}

// Read returns the most recent standup for a squad on the given date.
// Pass an empty date to read today's standup.
func (s *Store) Read(ctx context.Context, squad, date string) (*Report, error) {
	if date == "" {
		date = today()
	}
	data, err := s.rdb.Get(ctx, s.key(squad, date)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read standup %s/%s: %w", squad, date, err)
	}
	var r Report
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		return nil, fmt.Errorf("unmarshal standup: %w", err)
	}
	return &r, nil
}

// ReadAll returns all squad standups for the given date, sorted by squad name.
// Pass an empty date to read today's standups.
func (s *Store) ReadAll(ctx context.Context, date string) ([]Report, error) {
	if date == "" {
		date = today()
	}
	squads, err := s.rdb.SMembers(ctx, s.dateKey(date)).Result()
	if err != nil {
		return nil, fmt.Errorf("list squads for %s: %w", date, err)
	}
	sort.Strings(squads)

	reports := make([]Report, 0, len(squads))
	for _, squad := range squads {
		r, err := s.Read(ctx, squad, date)
		if err != nil || r == nil {
			continue
		}
		reports = append(reports, *r)
	}
	return reports, nil
}

func (s *Store) key(squad, date string) string {
	return fmt.Sprintf("%s:standup:%s:%s", s.ns, squad, date)
}

func (s *Store) dateKey(date string) string {
	return fmt.Sprintf("%s:standup-squads:%s", s.ns, date)
}

func today() string {
	return time.Now().UTC().Format("2006-01-02")
}
