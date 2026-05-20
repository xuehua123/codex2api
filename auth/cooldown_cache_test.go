package auth

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/cache"
)

func TestStoreSkipsCachedAccountCooldown(t *testing.T) {
	tokenCache := cache.NewMemory(4)
	defer tokenCache.Close()

	primary := newFastSchedulerTestAccount(1, HealthTierHealthy, 120, 1)
	fallback := newFastSchedulerTestAccount(2, HealthTierHealthy, 80, 1)
	fallback.LastFailureAt = time.Now()
	store := &Store{
		accounts:       []*Account{primary, fallback},
		maxConcurrency: 1,
		tokenCache:     tokenCache,
	}
	store.setCachedAccountCooldown(primary.DBID, "rate_limited", time.Now().Add(time.Hour))

	got := store.Next()
	if got == nil {
		t.Fatal("Next() returned nil")
	}
	defer store.Release(got)

	if got.DBID != fallback.DBID {
		t.Fatalf("Next() picked dbID=%d, want %d", got.DBID, fallback.DBID)
	}
	if primary.IsAvailable() {
		t.Fatal("primary account should have been synchronized into cooldown")
	}
}

func TestFastSchedulerSkipsCachedAccountCooldown(t *testing.T) {
	tokenCache := cache.NewMemory(4)
	defer tokenCache.Close()

	primary := newFastSchedulerTestAccount(1, HealthTierHealthy, 120, 1)
	fallback := newFastSchedulerTestAccount(2, HealthTierHealthy, 80, 1)
	fallback.LastFailureAt = time.Now()
	store := &Store{
		accounts:       []*Account{primary, fallback},
		maxConcurrency: 1,
		tokenCache:     tokenCache,
	}
	store.SetFastSchedulerEnabled(true)
	store.setCachedAccountCooldown(primary.DBID, "rate_limited", time.Now().Add(time.Hour))

	got := store.Next()
	if got == nil {
		t.Fatal("Next() returned nil")
	}
	defer store.Release(got)

	if got.DBID != fallback.DBID {
		t.Fatalf("Next() picked dbID=%d, want %d", got.DBID, fallback.DBID)
	}
	if primary.IsAvailable() {
		t.Fatal("primary account should have been synchronized into cooldown")
	}
}

func TestStoreSkipsCachedModelCooldown(t *testing.T) {
	tokenCache := cache.NewMemory(4)
	defer tokenCache.Close()

	primary := newFastSchedulerTestAccount(1, HealthTierHealthy, 120, 1)
	fallback := newFastSchedulerTestAccount(2, HealthTierHealthy, 80, 1)
	fallback.LastFailureAt = time.Now()
	store := &Store{
		accounts:       []*Account{primary, fallback},
		maxConcurrency: 1,
		tokenCache:     tokenCache,
	}
	store.setCachedModelCooldown(primary.DBID, ModelCooldown{
		Model:     "gpt-5.4",
		Reason:    "model_capacity",
		ResetAt:   time.Now().Add(time.Hour),
		UpdatedAt: time.Now(),
	})

	got := store.NextExcludingWithFilter(0, nil, store.WithModelCooldownFilter("GPT-5.4", nil))
	if got == nil {
		t.Fatal("NextExcludingWithFilter() returned nil")
	}
	defer store.Release(got)

	if got.DBID != fallback.DBID {
		t.Fatalf("NextExcludingWithFilter() picked dbID=%d, want %d", got.DBID, fallback.DBID)
	}
	if !primary.IsModelRateLimited("gpt-5.4") {
		t.Fatal("primary model cooldown should have been synchronized from runtime cache")
	}
}

func TestModelCooldownRuntimeMissesAreCachedBriefly(t *testing.T) {
	memoryCache := cache.NewMemory(4)
	defer memoryCache.Close()
	tokenCache := &countingRuntimeCache{TokenCache: memoryCache}

	primary := newFastSchedulerTestAccount(1, HealthTierHealthy, 120, 1)
	store := &Store{
		accounts:       []*Account{primary},
		maxConcurrency: 1,
		tokenCache:     tokenCache,
	}
	filter := store.WithModelCooldownFilter("gpt-5.4", nil)

	if !filter(primary) {
		t.Fatal("filter should allow account when runtime cache misses")
	}
	if !filter(primary) {
		t.Fatal("filter should allow account from negative miss cache")
	}
	if got := tokenCache.modelRuntimeGets.Load(); got != 1 {
		t.Fatalf("runtime model cooldown GETs = %d, want 1", got)
	}

	store.setCachedModelCooldown(primary.DBID, ModelCooldown{
		Model:     "gpt-5.4",
		Reason:    "model_capacity",
		ResetAt:   time.Now().Add(time.Hour),
		UpdatedAt: time.Now(),
	})
	if filter(primary) {
		t.Fatal("filter should reject account after runtime cooldown is written")
	}
	if got := tokenCache.modelRuntimeGets.Load(); got != 2 {
		t.Fatalf("runtime model cooldown GETs after write = %d, want 2", got)
	}
}

type countingRuntimeCache struct {
	cache.TokenCache
	modelRuntimeGets atomic.Int64
}

func (c *countingRuntimeCache) GetRuntime(ctx context.Context, namespace string, key string) (json.RawMessage, bool, error) {
	if namespace == modelCooldownCacheNamespace {
		c.modelRuntimeGets.Add(1)
	}
	return c.TokenCache.GetRuntime(ctx, namespace, key)
}

func TestCooldownCacheWritesAndDeletes(t *testing.T) {
	tokenCache := cache.NewMemory(4)
	defer tokenCache.Close()

	acc := newFastSchedulerTestAccount(1, HealthTierHealthy, 100, 1)
	store := &Store{
		accounts:       []*Account{acc},
		maxConcurrency: 1,
		tokenCache:     tokenCache,
	}

	store.MarkCooldown(acc, 5*time.Minute, "rate_limited")
	if _, ok, err := tokenCache.GetRuntime(context.Background(), accountCooldownCacheNamespace, accountCooldownRuntimeKey(acc.DBID)); err != nil || !ok {
		t.Fatalf("account cooldown runtime cache ok=%v err=%v, want ok", ok, err)
	}
	store.ClearCooldown(acc)
	if _, ok, err := tokenCache.GetRuntime(context.Background(), accountCooldownCacheNamespace, accountCooldownRuntimeKey(acc.DBID)); err != nil || ok {
		t.Fatalf("account cooldown runtime cache after clear ok=%v err=%v, want miss", ok, err)
	}

	store.MarkModelCooldown(acc, "gpt-5.4", 5*time.Minute, "model_capacity")
	if _, ok, err := tokenCache.GetRuntime(context.Background(), modelCooldownCacheNamespace, modelCooldownRuntimeKey(acc.DBID, "gpt-5.4")); err != nil || !ok {
		t.Fatalf("model cooldown runtime cache ok=%v err=%v, want ok", ok, err)
	}
	store.ClearModelCooldown(acc, "gpt-5.4")
	if _, ok, err := tokenCache.GetRuntime(context.Background(), modelCooldownCacheNamespace, modelCooldownRuntimeKey(acc.DBID, "gpt-5.4")); err != nil || ok {
		t.Fatalf("model cooldown runtime cache after clear ok=%v err=%v, want miss", ok, err)
	}
}
