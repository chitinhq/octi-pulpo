// Package standup provides async standup storage and formatting for the swarm.
// Each squad's EM posts a daily standup via the standup_report MCP tool.
// Standups are stored in Redis at {ns}:standup:{squad}:{YYYY-MM-DD} and
// expire after 30 days. The brain aggregates them into a unified Slack digest.
package standup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Entry represents a single squad's async standup for a given day.
type Entry struct {
	Squad    string   `json:"squad"`
	Done     []string `json:"done"`
	Doing    []string `json:"doing"`
	Blocked  []string `json:"blocked"`
	Requests []string `json:"requests"`
	PostedBy string   `json:"posted_by"`
	PostedAt string   `json:"posted_at"`
}

// Store manages async standup reports in Redis.
type Store struct {
	rdb       *redis.Client
	namespace string
}

// NewStore creates a standup store backed by Redis.
func NewStore(rdb *redis.Client, namespace string) *Store {
	return &Store{rdb: rdb, namespace: namespace}
}

// Report stores a squad's standup for today, overwriting any prior report.
func (s *Store) Report(ctx context.Context, squad, postedBy string, done, doing, blocked, requests []string) error {
	date := time.Now().UTC().Format("2006-01-02")
	key := s.key(squad, date)
	e := Entry{
		Squad:    squad,
		Done:     done,
		Doing:    doing,
		Blocked:  blocked,
		Requests: requests,
		PostedBy: postedBy,
		PostedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("standup: marshal: %w", err)
	}
	return s.rdb.Set(ctx, key, data, 30*24*time.Hour).Err()
}

// Read returns the standup for the given squad and date (YYYY-MM-DD).
// Returns nil entry (no error) if no standup has been filed.
func (s *Store) Read(ctx context.Context, squad, date string) (*Entry, error) {
	data, err := s.rdb.Get(ctx, s.key(squad, date)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("standup: read %s/%s: %w", squad, date, err)
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("standup: unmarshal: %w", err)
	}
	return &e, nil
}

// ReadToday returns all filed standups for the current UTC day.
func (s *Store) ReadToday(ctx context.Context) ([]Entry, error) {
	return s.ReadDate(ctx, time.Now().UTC().Format("2006-01-02"))
}

// ReadDate returns all filed standups for the given date (YYYY-MM-DD).
func (s *Store) ReadDate(ctx context.Context, date string) ([]Entry, error) {
	pattern := fmt.Sprintf("%s:standup:*:%s", s.namespace, date)
	keys, err := s.rdb.Keys(ctx, pattern).Result()
	if err != nil {
		return nil, fmt.Errorf("standup: scan %s: %w", date, err)
	}
	entries := make([]Entry, 0, len(keys))
	for _, key := range keys {
		data, err := s.rdb.Get(ctx, key).Bytes()
		if err != nil {
			continue
		}
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (s *Store) key(squad, date string) string {
	return fmt.Sprintf("%s:standup:%s:%s", s.namespace, squad, date)
}

// FormatSlack formats standup entries as a Slack mrkdwn digest.
func FormatSlack(date string, entries []Entry) string {
	if len(entries) == 0 {
		return fmt.Sprintf("*📋 Daily Standup — %s*\nNo standups filed yet.", date)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*📋 Daily Standup — %s*\n\n", date))

	for _, e := range entries {
		icon := "🟢"
		if len(e.Blocked) > 0 {
			icon = "🟡"
		}
		title := titleCase(e.Squad)
		sb.WriteString(fmt.Sprintf("%s *%s*\n", icon, title))
		if len(e.Done) > 0 {
			sb.WriteString(fmt.Sprintf("  *Done:* %s\n", strings.Join(e.Done, ", ")))
		}
		if len(e.Doing) > 0 {
			sb.WriteString(fmt.Sprintf("  *Doing:* %s\n", strings.Join(e.Doing, ", ")))
		}
		if len(e.Blocked) > 0 {
			sb.WriteString(fmt.Sprintf("  *Blocked:* %s\n", strings.Join(e.Blocked, ", ")))
		}
		if len(e.Requests) > 0 {
			sb.WriteString(fmt.Sprintf("  *Requests:* %s\n", strings.Join(e.Requests, ", ")))
		}
		sb.WriteString("\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
