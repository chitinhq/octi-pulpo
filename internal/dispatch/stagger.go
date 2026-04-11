package dispatch

import (
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// platformConfig holds per-platform stagger configuration.
type platformConfig struct {
	Cooldown time.Duration
	DailyCap int
}

// StaggerTracker enforces platform alternation and per-platform cooldowns.
type StaggerTracker struct {
	rdb       *redis.Client
	namespace string

	CopilotCooldown time.Duration
	ClaudeCooldown  time.Duration
	CopilotDailyCap int
	ClaudeDailyCap  int

	mu              sync.Mutex
	lastPlatform    string
	dispatches      map[string][]time.Time
	platformConfigs map[string]platformConfig
}

func NewStaggerTracker(rdb *redis.Client, namespace string) *StaggerTracker {
	return &StaggerTracker{
		rdb:             rdb,
		namespace:       namespace,
		CopilotCooldown: 30 * time.Minute,
		ClaudeCooldown:  45 * time.Minute,
		CopilotDailyCap: 8,
		ClaudeDailyCap:  6,
		dispatches:      make(map[string][]time.Time),
		platformConfigs: make(map[string]platformConfig),
	}
}

// RegisterPlatform registers a platform with a specific cooldown and daily cap.
// These values take precedence over the hardcoded claude/copilot defaults.
func (s *StaggerTracker) RegisterPlatform(name string, cooldown time.Duration, dailyCap int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.platformConfigs[name] = platformConfig{Cooldown: cooldown, DailyCap: dailyCap}
}

// NextPlatformFromList picks the first available platform from the priority list.
// avail maps platform name to whether it is available externally (e.g. quota not exhausted).
func (s *StaggerTracker) NextPlatformFromList(priority []string, avail map[string]bool) string {
	for _, p := range priority {
		if avail[p] {
			return p
		}
	}
	return ""
}

func (s *StaggerTracker) NextPlatform(copilotAvail, claudeAvail bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !copilotAvail && !claudeAvail {
		return ""
	}
	if !copilotAvail {
		return "claude"
	}
	if !claudeAvail {
		return "copilot"
	}
	if s.lastPlatform == "copilot" {
		return "claude"
	}
	return "copilot"
}

func (s *StaggerTracker) RecordDispatch(platform string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastPlatform = platform
	s.dispatches[platform] = append(s.dispatches[platform], at)
}

func (s *StaggerTracker) IsAvailable(platform string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	times := s.dispatches[platform]
	if len(times) == 0 {
		return true
	}
	last := times[len(times)-1]

	var cooldown time.Duration
	if cfg, ok := s.platformConfigs[platform]; ok {
		cooldown = cfg.Cooldown
	} else if platform == "claude" {
		cooldown = s.ClaudeCooldown
	} else {
		cooldown = s.CopilotCooldown
	}
	return now.Sub(last) >= cooldown
}

func (s *StaggerTracker) IsUnderDailyCap(platform string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	var cap int
	if cfg, ok := s.platformConfigs[platform]; ok {
		cap = cfg.DailyCap
	} else if platform == "claude" {
		cap = s.ClaudeDailyCap
	} else {
		cap = s.CopilotDailyCap
	}

	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	count := 0
	for _, t := range s.dispatches[platform] {
		if t.After(startOfDay) {
			count++
		}
	}
	return count < cap
}
