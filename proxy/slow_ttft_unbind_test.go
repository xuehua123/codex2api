package proxy

import (
	"testing"
	"time"
)

func TestSlowTTFTUnbindControllerUnbindsTopPercentileWhenEnabled(t *testing.T) {
	controller := newTestSlowTTFTUnbindController(slowTTFTUnbindConfig{
		enabled:                 true,
		window:                  5 * time.Minute,
		triggerPercentile:       95,
		observePercentile:       90,
		globalBadAccountPercent: 100,
		minSamples:              20,
		affinityCooldown:        10 * time.Minute,
		globalMinAccounts:       0,
	})

	for i := 0; i < 19; i++ {
		controller.record("/v1/responses", "gpt-5.5", "xhigh", true, int64(i+1), "session-low", 1000+i)
	}
	decision := controller.record("/v1/responses", "gpt-5.5", "xhigh", true, 20, "session-slow", 60_000)

	if decision.action != "unbind" {
		t.Fatalf("action = %q, want unbind; decision=%+v", decision.action, decision)
	}
	if !decision.shouldUnbind {
		t.Fatalf("shouldUnbind = false, want true")
	}
}

func TestSlowTTFTUnbindControllerObserveModeDoesNotUnbind(t *testing.T) {
	controller := newTestSlowTTFTUnbindController(slowTTFTUnbindConfig{
		enabled:                 false,
		window:                  5 * time.Minute,
		triggerPercentile:       95,
		observePercentile:       90,
		globalBadAccountPercent: 100,
		minSamples:              20,
		affinityCooldown:        10 * time.Minute,
		globalMinAccounts:       0,
	})

	for i := 0; i < 19; i++ {
		controller.record("/v1/responses", "gpt-5.5", "xhigh", true, int64(i+1), "session-low", 1000+i)
	}
	decision := controller.record("/v1/responses", "gpt-5.5", "xhigh", true, 20, "session-slow", 60_000)

	if decision.action != "disabled" {
		t.Fatalf("action = %q, want disabled; decision=%+v", decision.action, decision)
	}
	if decision.shouldUnbind {
		t.Fatalf("shouldUnbind = true, want false")
	}
}

func TestSlowTTFTUnbindControllerGlobalProtectionPausesOnlySlowUnbind(t *testing.T) {
	controller := newTestSlowTTFTUnbindController(slowTTFTUnbindConfig{
		enabled:                 true,
		window:                  5 * time.Minute,
		triggerPercentile:       80,
		observePercentile:       70,
		globalBadAccountPercent: 30,
		minSamples:              6,
		affinityCooldown:        0,
		globalMinAccounts:       3,
	})

	for i := 0; i < 5; i++ {
		controller.record("/v1/responses", "gpt-5.5", "xhigh", true, int64(i+1), "session-low", 1000+i)
	}
	first := controller.record("/v1/responses", "gpt-5.5", "xhigh", true, 1, "session-slow-1", 60_000)
	second := controller.record("/v1/responses", "gpt-5.5", "xhigh", true, 2, "session-slow-2", 61_000)

	if first.action != "unbind" {
		t.Fatalf("first action = %q, want unbind; decision=%+v", first.action, first)
	}
	if second.action != "paused" {
		t.Fatalf("second action = %q, want paused; decision=%+v", second.action, second)
	}
	if second.shouldUnbind {
		t.Fatalf("second shouldUnbind = true, want false")
	}
}

func TestSlowTTFTUnbindControllerAffinityCooldownLimitsRepeatedUnbinds(t *testing.T) {
	now := time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC)
	controller := newTestSlowTTFTUnbindController(slowTTFTUnbindConfig{
		enabled:                 true,
		window:                  5 * time.Minute,
		triggerPercentile:       80,
		observePercentile:       70,
		globalBadAccountPercent: 100,
		minSamples:              6,
		affinityCooldown:        10 * time.Minute,
		globalMinAccounts:       0,
	})
	controller.timeNow = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		controller.record("/v1/responses", "gpt-5.5", "xhigh", true, int64(i+1), "session-low", 1000+i)
	}
	first := controller.record("/v1/responses", "gpt-5.5", "xhigh", true, 1, "session-slow", 60_000)
	now = now.Add(time.Minute)
	second := controller.record("/v1/responses", "gpt-5.5", "xhigh", true, 1, "session-slow", 61_000)

	if first.action != "unbind" {
		t.Fatalf("first action = %q, want unbind; decision=%+v", first.action, first)
	}
	if second.action != "cooldown" {
		t.Fatalf("second action = %q, want cooldown; decision=%+v", second.action, second)
	}
	if second.shouldUnbind {
		t.Fatalf("second shouldUnbind = true, want false")
	}
}

func TestSlowTTFTUnbindControllerRequiresCohortMinimumSamples(t *testing.T) {
	controller := newTestSlowTTFTUnbindController(slowTTFTUnbindConfig{
		enabled:                 true,
		window:                  5 * time.Minute,
		triggerPercentile:       95,
		observePercentile:       90,
		globalBadAccountPercent: 100,
		minSamples:              20,
		affinityCooldown:        10 * time.Minute,
		globalMinAccounts:       0,
	})

	for i := 0; i < 5; i++ {
		controller.record("/v1/responses", "gpt-5.5", "xhigh", true, int64(i+1), "session-low", 1000+i)
	}
	decision := controller.record("/v1/responses", "gpt-5.5", "xhigh", true, 6, "session-slow", 60_000)

	if decision.action != "insufficient_samples" {
		t.Fatalf("action = %q, want insufficient_samples; decision=%+v", decision.action, decision)
	}
	if decision.shouldUnbind {
		t.Fatalf("shouldUnbind = true, want false")
	}
}

func TestSlowTTFTUnbindControllerDoesNotTreatAllTiesAsTopPercentile(t *testing.T) {
	controller := newTestSlowTTFTUnbindController(slowTTFTUnbindConfig{
		enabled:                 true,
		window:                  5 * time.Minute,
		triggerPercentile:       95,
		observePercentile:       90,
		globalBadAccountPercent: 100,
		minSamples:              20,
		affinityCooldown:        10 * time.Minute,
		globalMinAccounts:       0,
	})

	var decision slowTTFTUnbindDecision
	for i := 0; i < 20; i++ {
		decision = controller.record("/v1/responses", "gpt-5.5", "xhigh", true, int64(i+1), "session-tie", 1000)
	}

	if decision.action != "normal" {
		t.Fatalf("action = %q, want normal; decision=%+v", decision.action, decision)
	}
	if decision.shouldUnbind {
		t.Fatalf("shouldUnbind = true, want false")
	}
}

func newTestSlowTTFTUnbindController(cfg slowTTFTUnbindConfig) *slowTTFTUnbindController {
	controller := newSlowTTFTUnbindController(cfg)
	controller.timeNow = func() time.Time {
		return time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC)
	}
	return controller
}
