package auth

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/alerting"
)

func TestAccountPoolDiagnosticsAvailableMatchesStoreAvailability(t *testing.T) {
	now := time.Now()
	store := &Store{accounts: []*Account{
		{
			DBID:        1,
			AccessToken: "token-active",
			Status:      StatusReady,
			PlanType:    "plus",
		},
		{
			DBID:         2,
			AccessToken:  "token-expired-cooldown",
			Status:       StatusCooldown,
			CooldownUtil: now.Add(-time.Minute),
			PlanType:     "plus",
		},
		{
			DBID:           3,
			AccessToken:    "token-active-cooldown",
			Status:         StatusCooldown,
			CooldownReason: "rate_limited",
			CooldownUtil:   now.Add(time.Hour),
			PlanType:       "plus",
		},
	}}

	diagnostics := store.AccountPoolDiagnostics()
	if diagnostics.Snapshot.Available != store.AvailableCount() {
		t.Fatalf("diagnostic available = %d, AvailableCount = %d", diagnostics.Snapshot.Available, store.AvailableCount())
	}
	if diagnostics.Snapshot.Available != 2 {
		t.Fatalf("diagnostic available = %d, want 2", diagnostics.Snapshot.Available)
	}
}

func TestAccountPoolDiagnosticsSeparatesRateLimitWindows(t *testing.T) {
	now := time.Now()
	store := &Store{accounts: []*Account{
		{
			DBID:                1,
			AccessToken:         "token-5h",
			Status:              StatusReady,
			PlanType:            "plus",
			UsagePercent5hValid: true,
			UsagePercent5h:      100,
			Reset5hAt:           now.Add(time.Hour),
		},
		{
			DBID:                2,
			AccessToken:         "token-7d",
			Status:              StatusReady,
			PlanType:            "free",
			UsagePercent7dValid: true,
			UsagePercent7d:      100,
		},
		{
			DBID:           3,
			AccessToken:    "token-generic",
			Status:         StatusCooldown,
			CooldownReason: "rate_limited",
			CooldownUtil:   now.Add(time.Hour),
			PlanType:       "team",
		},
	}}

	got := diagnosticItemsByKey(store.AccountPoolDiagnostics().Issues)
	if got["rate_limited_5h"] != 1 {
		t.Fatalf("rate_limited_5h = %d, want 1", got["rate_limited_5h"])
	}
	if got["usage_7d_exhausted"] != 1 {
		t.Fatalf("usage_7d_exhausted = %d, want 1", got["usage_7d_exhausted"])
	}
	if got["rate_limited_generic"] != 1 {
		t.Fatalf("rate_limited_generic = %d, want 1", got["rate_limited_generic"])
	}
	if got["rate_limited_7d_or_generic"] != 0 {
		t.Fatalf("rate_limited_7d_or_generic = %d, want removed bucket", got["rate_limited_7d_or_generic"])
	}
}

func TestAccountPoolDiagnosticsClassifiesDisabledAfterSpecificCauses(t *testing.T) {
	account := &Account{
		DBID:           1,
		AccessToken:    "token",
		Status:         StatusCooldown,
		CooldownReason: "unauthorized",
		CooldownUtil:   time.Now().Add(time.Hour),
		PlanType:       "plus",
	}
	atomic.StoreInt32(&account.Disabled, 1)
	store := &Store{accounts: []*Account{account}}

	got := diagnosticItemsByKey(store.AccountPoolDiagnostics().Issues)
	if got["unauthorized"] != 1 {
		t.Fatalf("unauthorized = %d, want 1", got["unauthorized"])
	}
	if got["temporarily_disabled"] != 0 {
		t.Fatalf("temporarily_disabled = %d, want 0 when unauthorized is known", got["temporarily_disabled"])
	}
}

func diagnosticItemsByKey(items []alerting.DiagnosticItem) map[string]int {
	got := make(map[string]int, len(items))
	for _, item := range items {
		got[item.Key] = item.Count
	}
	return got
}
