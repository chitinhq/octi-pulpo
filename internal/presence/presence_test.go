package presence

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func testSetup(t *testing.T, ttl time.Duration) (*Store, context.Context) {
	t.Helper()

	redisURL := os.Getenv("OCTI_REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}

	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Skipf("skipping: cannot parse redis URL: %v", err)
	}
	rdb := redis.NewClient(opts)
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: redis not available: %v", err)
	}

	ns := "octi-test-presence-" + t.Name()

	store := New(rdb, ns, ttl)

	cleanup := func() {
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
	}
	cleanup()
	t.Cleanup(func() {
		cleanup()
		rdb.Close()
	})

	return store, ctx
}

// TestPresence_PublishAndGet verifies a SET then GET round trip.
func TestPresence_PublishAndGet(t *testing.T) {
	store, ctx := testSetup(t, 0)

	if err := store.Publish(ctx, "alice", Focused); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	state, err := store.Get(ctx, "alice")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if state != Focused {
		t.Errorf("expected Focused, got %q", state)
	}

	// Overwrite with Unfocused.
	if err := store.Publish(ctx, "alice", Unfocused); err != nil {
		t.Fatalf("Publish Unfocused: %v", err)
	}
	state, err = store.Get(ctx, "alice")
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}
	if state != Unfocused {
		t.Errorf("expected Unfocused after overwrite, got %q", state)
	}
}

// TestPresence_DefaultsToUnfocused verifies that GET without a prior SET returns Unfocused.
func TestPresence_DefaultsToUnfocused(t *testing.T) {
	store, ctx := testSetup(t, 0)

	state, err := store.Get(ctx, "ghost")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if state != Unfocused {
		t.Errorf("expected Unfocused for unknown user, got %q", state)
	}

	active, err := store.IsActive(ctx, "ghost")
	if err != nil {
		t.Fatalf("IsActive: %v", err)
	}
	if active {
		t.Error("expected IsActive=false for unknown user")
	}
}

// TestPresence_Refresh verifies that Refresh extends the TTL on an existing key.
func TestPresence_Refresh(t *testing.T) {
	// Use a short TTL so we can observe it.
	store, ctx := testSetup(t, 2*time.Second)

	if err := store.Publish(ctx, "bob", Focused); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Wait a bit so TTL has decremented.
	time.Sleep(500 * time.Millisecond)

	// Refresh should succeed and reset the TTL back to 2s.
	if err := store.Refresh(ctx, "bob"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// The key should still be Focused after refresh.
	state, err := store.Get(ctx, "bob")
	if err != nil {
		t.Fatalf("Get after Refresh: %v", err)
	}
	if state != Focused {
		t.Errorf("expected Focused after Refresh, got %q", state)
	}

	// TTL should be close to 2s again (not near zero).
	ttl, err := store.rdb.TTL(ctx, store.key("bob")).Result()
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	if ttl < time.Second {
		t.Errorf("expected TTL >= 1s after Refresh, got %v", ttl)
	}
}

// TestPresence_TTLExpiry verifies that a key with 1s TTL becomes Unfocused after 2s.
func TestPresence_TTLExpiry(t *testing.T) {
	store, ctx := testSetup(t, 1*time.Second)

	if err := store.Publish(ctx, "carol", Focused); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Confirm initially focused.
	state, err := store.Get(ctx, "carol")
	if err != nil {
		t.Fatalf("Get before expiry: %v", err)
	}
	if state != Focused {
		t.Fatalf("expected Focused before expiry, got %q", state)
	}

	// Wait for TTL to expire.
	time.Sleep(2 * time.Second)

	state, err = store.Get(ctx, "carol")
	if err != nil {
		t.Fatalf("Get after expiry: %v", err)
	}
	if state != Unfocused {
		t.Errorf("expected Unfocused after TTL expiry, got %q", state)
	}
}
