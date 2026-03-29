package memory

import (
	"context"
	"encoding/json"
	"fmt"
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
type Store struct {
	rdb *redis.Client
	ns  string
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

// Put stores a memory entry. Returns the entry ID.
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
	return id, err
}

// Recall searches memories by keyword matching. Vector search planned.
func (s *Store) Recall(ctx context.Context, query string, limit int) ([]Entry, error) {
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

// Close shuts down the Redis connection.
func (s *Store) Close() error {
	return s.rdb.Close()
}

func (s *Store) key(suffix string) string {
	return s.ns + ":" + suffix
}

func randomSuffix() string {
	return fmt.Sprintf("%06x", time.Now().UnixNano()&0xFFFFFF)
}
