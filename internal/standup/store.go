package standup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Entry is a squad's async standup for a single day.
type Entry struct {
	Squad     string    `json:"squad"`
	Date      string    `json:"date"`      // YYYY-MM-DD
	Done      []string  `json:"done"`
	Doing     []string  `json:"doing"`
	Blocked   []string  `json:"blocked"`
	Requests  []string  `json:"requests"`
	Timestamp time.Time `json:"timestamp"`
}

// Store persists async standup entries in Redis.
// Key scheme: {namespace}:standup:{squad}:{YYYY-MM-DD}
// TTL: 7 days (standups are ephemeral reference data).
type Store struct {
	rdb       *redis.Client
	namespace string
}

const standupTTL = 7 * 24 * time.Hour

// New creates a standup store backed by Redis.
func New(rdb *redis.Client, namespace string) *Store {
	return &Store{rdb: rdb, namespace: namespace}
}

// Report stores (or replaces) a squad's standup for today.
func (s *Store) Report(ctx context.Context, entry Entry) error {
	if entry.Squad == "" {
		return fmt.Errorf("squad is required")
	}
	entry.Date = today()
	entry.Timestamp = time.Now().UTC()
	// Ensure slices serialize as [] not null.
	if entry.Done == nil {
		entry.Done = []string{}
	}
	if entry.Doing == nil {
		entry.Doing = []string{}
	}
	if entry.Blocked == nil {
		entry.Blocked = []string{}
	}
	if entry.Requests == nil {
		entry.Requests = []string{}
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal standup entry: %w", err)
	}
	return s.rdb.Set(ctx, s.key(entry.Squad, entry.Date), data, standupTTL).Err()
}

// Read returns today's standup for the given squad.
// Returns nil, nil if no entry exists.
func (s *Store) Read(ctx context.Context, squad string) (*Entry, error) {
	return s.ReadDate(ctx, squad, today())
}

// ReadDate returns the standup for the given squad and date (YYYY-MM-DD).
// Returns nil, nil if no entry exists.
func (s *Store) ReadDate(ctx context.Context, squad, date string) (*Entry, error) {
	raw, err := s.rdb.Get(ctx, s.key(squad, date)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get standup %s/%s: %w", squad, date, err)
	}
	var entry Entry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		return nil, fmt.Errorf("parse standup entry: %w", err)
	}
	return &entry, nil
}

// Daily returns all squad standup entries for today.
func (s *Store) Daily(ctx context.Context) ([]Entry, error) {
	pattern := fmt.Sprintf("%s:standup:*:%s", s.namespace, today())
	keys, err := s.rdb.Keys(ctx, pattern).Result()
	if err != nil {
		return nil, fmt.Errorf("scan standup keys: %w", err)
	}
	if len(keys) == 0 {
		return nil, nil
	}

	vals, err := s.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("mget standups: %w", err)
	}

	var entries []Entry
	for _, v := range vals {
		if v == nil {
			continue
		}
		str, ok := v.(string)
		if !ok {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(str), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (s *Store) key(squad, date string) string {
	return fmt.Sprintf("%s:standup:%s:%s", s.namespace, squad, date)
}

func today() string {
	return time.Now().UTC().Format("2006-01-02")
}
