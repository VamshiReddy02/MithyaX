package sessioncred_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/vamshireddy02/mithyax/gateway/internal/sessioncred"
)

func TestStore_IssueThenValidate(t *testing.T) {
	client := newTestRedis(t)
	store := sessioncred.NewStore(client, time.Hour)

	cred, err := store.Issue(context.Background())
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if cred.Token == "" {
		t.Fatal("Issue() returned an empty token")
	}
	if !cred.ExpiresAt.After(time.Now()) {
		t.Errorf("ExpiresAt = %v, want a time in the future", cred.ExpiresAt)
	}

	valid, err := store.Validate(context.Background(), cred.Token)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !valid {
		t.Error("Validate() = false for a just-issued credential, want true")
	}
}

func TestStore_ValidateUnknownToken(t *testing.T) {
	client := newTestRedis(t)
	store := sessioncred.NewStore(client, time.Hour)

	valid, err := store.Validate(context.Background(), "never-issued")
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if valid {
		t.Error("Validate() = true for a token that was never issued, want false")
	}
}

func TestStore_ValidateEmptyToken(t *testing.T) {
	client := newTestRedis(t)
	store := sessioncred.NewStore(client, time.Hour)

	valid, err := store.Validate(context.Background(), "")
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if valid {
		t.Error("Validate() = true for an empty token, want false")
	}
}

// TestStore_IssueGeneratesDistinctTokens guards against the credential
// generator ever producing a predictable or repeated value — each
// Issue() must be independently unguessable.
func TestStore_IssueGeneratesDistinctTokens(t *testing.T) {
	client := newTestRedis(t)
	store := sessioncred.NewStore(client, time.Hour)

	first, err := store.Issue(context.Background())
	if err != nil {
		t.Fatalf("first Issue() error = %v", err)
	}
	second, err := store.Issue(context.Background())
	if err != nil {
		t.Fatalf("second Issue() error = %v", err)
	}
	if first.Token == second.Token {
		t.Error("two Issue() calls returned the same token")
	}
}

func TestStore_TTLExpiry(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run() error = %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	store := sessioncred.NewStore(client, 50*time.Millisecond)
	cred, err := store.Issue(context.Background())
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	if valid, err := store.Validate(context.Background(), cred.Token); err != nil || !valid {
		t.Fatalf("Validate() before TTL expiry = (%v, %v), want (true, nil)", valid, err)
	}

	mr.FastForward(100 * time.Millisecond)

	valid, err := store.Validate(context.Background(), cred.Token)
	if err != nil {
		t.Fatalf("Validate() after TTL expiry error = %v", err)
	}
	if valid {
		t.Error("Validate() after TTL expiry = true, want false")
	}
}

func TestStore_Issue_RedisUnavailable(t *testing.T) {
	client := unreachableRedis(t)
	store := sessioncred.NewStore(client, time.Hour)

	_, err := store.Issue(context.Background())
	if !errors.Is(err, sessioncred.ErrRedisUnavailable) {
		t.Errorf("Issue() error = %v, want ErrRedisUnavailable", err)
	}
}

func TestStore_Validate_RedisUnavailable(t *testing.T) {
	client := unreachableRedis(t)
	store := sessioncred.NewStore(client, time.Hour)

	_, err := store.Validate(context.Background(), "some-token")
	if !errors.Is(err, sessioncred.ErrRedisUnavailable) {
		t.Errorf("Validate() error = %v, want ErrRedisUnavailable", err)
	}
}
