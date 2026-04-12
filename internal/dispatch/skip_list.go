package dispatch

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const defaultSkipThreshold = 3
const defaultSkipTTL = 24 * time.Hour

// SkipEntry records why and until when an issue is skipped.
type SkipEntry struct {
	AddedAt   time.Time
	ExpiresAt time.Time
	Reason    string
}

// SkipList tracks issues that no platform can dispatch.
// Uses in-memory maps with optional Redis persistence.
type SkipList struct {
	rdb       *redis.Client
	namespace string
	Threshold int
	TTL       time.Duration

	mu         sync.Mutex
	rejections map[string]int       // issue key -> consecutive rejection count
	skipped    map[string]SkipEntry // issue key -> skip entry with expiry + reason
}

// NewSkipList creates a skip list. If rdb is nil, operates in-memory only.
func NewSkipList(rdb *redis.Client, namespace string) *SkipList {
	return &SkipList{
		rdb:        rdb,
		namespace:  namespace,
		Threshold:  defaultSkipThreshold,
		TTL:        defaultSkipTTL,
		rejections: make(map[string]int),
		skipped:    make(map[string]SkipEntry),
	}
}

// LoadFromRedis hydrates the in-memory skip list from Redis on startup.
// Stored score is the expiry unix timestamp.
func (s *SkipList) LoadFromRedis() int {
	if s.rdb == nil {
		return 0
	}
	ctx := context.Background()
	key := fmt.Sprintf("%s:skip-list", s.namespace)
	members, err := s.rdb.ZRangeWithScores(ctx, key, 0, -1).Result()
	if err != nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range members {
		issueKey := m.Member.(string)
		expiresAt := time.Unix(int64(m.Score), 0)
		s.skipped[issueKey] = SkipEntry{
			AddedAt:   time.Now(),
			ExpiresAt: expiresAt,
			Reason:    "loaded-from-redis",
		}
		s.rejections[issueKey] = s.Threshold // already skipped
	}
	return len(members)
}

// RecordRejection increments the rejection counter. If it hits the threshold,
// the issue is added to the skip list with the default TTL.
func (s *SkipList) RecordRejection(issueKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rejections[issueKey]++
	if s.rejections[issueKey] >= s.Threshold {
		now := time.Now()
		entry := SkipEntry{
			AddedAt:   now,
			ExpiresAt: now.Add(s.TTL),
			Reason:    "no-platform-accepts",
		}
		s.skipped[issueKey] = entry
		s.persistEntry(issueKey, entry)
	}
}

// SkipFor immediately adds an issue to the skip list with a custom TTL and
// reason. Bypasses the rejection threshold — used for environmental blocks
// like "repo has uncommitted changes" or "budget exhausted" that should
// prevent re-dispatch until the condition likely resolves.
func (s *SkipList) SkipFor(issueKey, reason string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	entry := SkipEntry{
		AddedAt:   now,
		ExpiresAt: now.Add(ttl),
		Reason:    reason,
	}
	s.skipped[issueKey] = entry
	s.persistEntry(issueKey, entry)
}

// persistEntry writes a skip entry to Redis. Score is the expiry unix
// timestamp so ExpireOld can use ZRemRangeByScore efficiently. Caller must
// hold s.mu.
func (s *SkipList) persistEntry(issueKey string, entry SkipEntry) {
	if s.rdb == nil {
		return
	}
	ctx := context.Background()
	key := fmt.Sprintf("%s:skip-list", s.namespace)
	s.rdb.ZAdd(ctx, key, redis.Z{
		Score:  float64(entry.ExpiresAt.Unix()),
		Member: issueKey,
	})
}

// IsSkipped returns true if the issue is currently skipped (not yet expired).
func (s *SkipList) IsSkipped(issueKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.skipped[issueKey]
	if !ok {
		return false
	}
	if time.Now().After(entry.ExpiresAt) {
		// Lazy expiry — clean up on access
		delete(s.skipped, issueKey)
		delete(s.rejections, issueKey)
		if s.rdb != nil {
			ctx := context.Background()
			key := fmt.Sprintf("%s:skip-list", s.namespace)
			s.rdb.ZRem(ctx, key, issueKey)
		}
		return false
	}
	return true
}

// SkipReason returns the reason an issue was skipped, or empty string if not
// skipped. Useful for telemetry and dashboards.
func (s *SkipList) SkipReason(issueKey string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.skipped[issueKey]; ok {
		return entry.Reason
	}
	return ""
}

// Clear removes an issue from the skip list and resets its rejection counter.
func (s *SkipList) Clear(issueKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.skipped, issueKey)
	delete(s.rejections, issueKey)
	if s.rdb != nil {
		ctx := context.Background()
		key := fmt.Sprintf("%s:skip-list", s.namespace)
		s.rdb.ZRem(ctx, key, issueKey)
	}
}

// ClearAll removes all entries from the skip list.
func (s *SkipList) ClearAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipped = make(map[string]SkipEntry)
	s.rejections = make(map[string]int)
	if s.rdb != nil {
		ctx := context.Background()
		key := fmt.Sprintf("%s:skip-list", s.namespace)
		s.rdb.Del(ctx, key)
	}
}

// ExpireOld removes entries whose per-entry ExpiresAt has passed.
func (s *SkipList) ExpireOld() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, entry := range s.skipped {
		if now.After(entry.ExpiresAt) {
			delete(s.skipped, k)
			delete(s.rejections, k)
		}
	}
	if s.rdb != nil {
		ctx := context.Background()
		key := fmt.Sprintf("%s:skip-list", s.namespace)
		s.rdb.ZRemRangeByScore(ctx, key, "-inf", fmt.Sprintf("%d", now.Unix()))
	}
}

// ListAll returns all currently skipped issue keys.
func (s *SkipList) ListAll() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.skipped {
		keys = append(keys, k)
	}
	return keys
}

// Size returns the number of skipped issues.
func (s *SkipList) Size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.skipped)
}
