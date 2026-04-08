package presence

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// State represents a user's presence state.
type State string

const (
	// Focused indicates the user is actively engaged.
	Focused State = "focused"
	// Unfocused indicates the user is away or idle.
	Unfocused State = "unfocused"
)

// defaultTTL is the default key expiry if none is provided.
const defaultTTL = 5 * time.Minute

// Store manages user presence signals backed by Redis TTL keys.
type Store struct {
	rdb       *redis.Client
	namespace string
	ttl       time.Duration
}

// New creates a presence Store. If ttl is zero, a default of 5 minutes is used.
func New(rdb *redis.Client, namespace string, ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	return &Store{
		rdb:       rdb,
		namespace: namespace,
		ttl:       ttl,
	}
}

func (s *Store) key(user string) string {
	return fmt.Sprintf("%s:presence:%s", s.namespace, user)
}

// Publish sets the presence state for a user with TTL.
// Redis key: {namespace}:presence:{user}
func (s *Store) Publish(ctx context.Context, user string, state State) error {
	return s.rdb.Set(ctx, s.key(user), string(state), s.ttl).Err()
}

// Refresh extends the TTL on an existing presence key without changing the value.
// Returns an error if the key does not exist (expired or never set).
func (s *Store) Refresh(ctx context.Context, user string) error {
	ok, err := s.rdb.Expire(ctx, s.key(user), s.ttl).Result()
	if err != nil {
		return fmt.Errorf("presence refresh: %w", err)
	}
	if !ok {
		return fmt.Errorf("presence refresh: key not found for user %q", user)
	}
	return nil
}

// Get returns the presence state for a user. Returns Unfocused if the key
// does not exist or has expired.
func (s *Store) Get(ctx context.Context, user string) (State, error) {
	val, err := s.rdb.Get(ctx, s.key(user)).Result()
	if err == redis.Nil {
		return Unfocused, nil
	}
	if err != nil {
		return Unfocused, fmt.Errorf("presence get: %w", err)
	}
	if val == "" {
		return Unfocused, nil
	}
	return State(val), nil
}

// IsActive is shorthand for Get() == Focused.
func (s *Store) IsActive(ctx context.Context, user string) (bool, error) {
	state, err := s.Get(ctx, user)
	if err != nil {
		return false, err
	}
	return state == Focused, nil
}
