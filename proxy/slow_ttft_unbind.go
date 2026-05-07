package proxy

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultSlowTTFTWindow                  = 5 * time.Minute
	defaultSlowTTFTTriggerPercentile       = 95.0
	defaultSlowTTFTObservePercentile       = 90.0
	defaultSlowTTFTGlobalBadAccountPercent = 30.0
	defaultSlowTTFTMinSamples              = 50
	defaultSlowTTFTAffinityCooldown        = 10 * time.Minute
	defaultSlowTTFTGlobalMinAccounts       = 10
)

type slowTTFTUnbindConfig struct {
	enabled                 bool
	window                  time.Duration
	triggerPercentile       float64
	observePercentile       float64
	globalBadAccountPercent float64
	minSamples              int
	affinityCooldown        time.Duration
	globalMinAccounts       int
}

type slowTTFTSample struct {
	at            time.Time
	cohort        string
	accountID     int64
	firstTokenMs  int
	slowCandidate bool
}

type slowTTFTUnbindDecision struct {
	action                  string
	cohort                  string
	firstTokenMs            int
	percentile              float64
	sampleCount             int
	activeAccounts          int
	badAccounts             int
	globalBadAccountPercent float64
	enabled                 bool
	shouldUnbind            bool
}

type slowTTFTUnbindController struct {
	mu           sync.Mutex
	cfg          slowTTFTUnbindConfig
	samples      []slowTTFTSample
	lastUnbound  map[string]time.Time
	timeNow      func() time.Time
	decisionHook func(slowTTFTUnbindDecision)
}

func newSlowTTFTUnbindControllerFromEnv() *slowTTFTUnbindController {
	return newSlowTTFTUnbindController(loadSlowTTFTUnbindConfigFromEnv())
}

func newSlowTTFTUnbindController(cfg slowTTFTUnbindConfig) *slowTTFTUnbindController {
	cfg = normalizeSlowTTFTUnbindConfig(cfg)
	return &slowTTFTUnbindController{
		cfg:         cfg,
		lastUnbound: make(map[string]time.Time),
		timeNow:     time.Now,
	}
}

func loadSlowTTFTUnbindConfigFromEnv() slowTTFTUnbindConfig {
	return slowTTFTUnbindConfig{
		enabled:                 parseBoolEnv("CODEX_SLOW_TTFT_UNBIND_ENABLED", false),
		window:                  parseDurationEnv("CODEX_SLOW_TTFT_WINDOW", defaultSlowTTFTWindow),
		triggerPercentile:       parseFloatEnv("CODEX_SLOW_TTFT_TRIGGER_PERCENTILE", defaultSlowTTFTTriggerPercentile),
		observePercentile:       parseFloatEnv("CODEX_SLOW_TTFT_OBSERVE_PERCENTILE", defaultSlowTTFTObservePercentile),
		globalBadAccountPercent: parseFloatEnv("CODEX_SLOW_TTFT_GLOBAL_BAD_ACCOUNT_PERCENT", defaultSlowTTFTGlobalBadAccountPercent),
		minSamples:              parseIntEnv("CODEX_SLOW_TTFT_MIN_SAMPLES", defaultSlowTTFTMinSamples),
		affinityCooldown:        parseDurationEnv("CODEX_SLOW_TTFT_AFFINITY_COOLDOWN", defaultSlowTTFTAffinityCooldown),
		globalMinAccounts:       parseIntEnv("CODEX_SLOW_TTFT_GLOBAL_MIN_ACCOUNTS", defaultSlowTTFTGlobalMinAccounts),
	}
}

func normalizeSlowTTFTUnbindConfig(cfg slowTTFTUnbindConfig) slowTTFTUnbindConfig {
	if cfg.window <= 0 {
		cfg.window = defaultSlowTTFTWindow
	}
	if cfg.triggerPercentile <= 0 || cfg.triggerPercentile > 100 {
		cfg.triggerPercentile = defaultSlowTTFTTriggerPercentile
	}
	if cfg.observePercentile <= 0 || cfg.observePercentile > 100 {
		cfg.observePercentile = defaultSlowTTFTObservePercentile
	}
	if cfg.observePercentile > cfg.triggerPercentile {
		cfg.observePercentile = cfg.triggerPercentile
	}
	if cfg.globalBadAccountPercent < 0 || cfg.globalBadAccountPercent > 100 {
		cfg.globalBadAccountPercent = defaultSlowTTFTGlobalBadAccountPercent
	}
	if cfg.minSamples <= 0 {
		cfg.minSamples = defaultSlowTTFTMinSamples
	}
	if cfg.affinityCooldown < 0 {
		cfg.affinityCooldown = defaultSlowTTFTAffinityCooldown
	}
	if cfg.globalMinAccounts < 0 {
		cfg.globalMinAccounts = defaultSlowTTFTGlobalMinAccounts
	}
	return cfg
}

func parseBoolEnv(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	switch strings.ToLower(value) {
	case "1", "true", "yes", "on", "enabled":
		return true
	case "0", "false", "no", "off", "disabled":
		return false
	default:
		return fallback
	}
}

func parseDurationEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func parseFloatEnv(key string, fallback float64) float64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseIntEnv(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func slowTTFTCohort(endpoint, model, reasoningEffort string, stream bool) string {
	return fmt.Sprintf("%s|%s|%s|stream=%t",
		normalizeSlowTTFTCohortPart(endpoint, "unknown-endpoint"),
		normalizeSlowTTFTCohortPart(model, "unknown-model"),
		normalizeSlowTTFTCohortPart(reasoningEffort, "default-effort"),
		stream,
	)
}

func normalizeSlowTTFTCohortPart(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	return value
}

func (c *slowTTFTUnbindController) record(endpoint, model, reasoningEffort string, stream bool, accountID int64, affinityKey string, firstTokenMs int) slowTTFTUnbindDecision {
	if c == nil || firstTokenMs <= 0 {
		return slowTTFTUnbindDecision{action: "ignored", firstTokenMs: firstTokenMs}
	}
	now := c.now()
	cohort := slowTTFTCohort(endpoint, model, reasoningEffort, stream)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.pruneLocked(now)
	sample := slowTTFTSample{
		at:           now,
		cohort:       cohort,
		accountID:    accountID,
		firstTokenMs: firstTokenMs,
	}
	c.samples = append(c.samples, sample)
	sampleIndex := len(c.samples) - 1

	percentile, sampleCount := c.percentileRankLocked(cohort, firstTokenMs)
	decision := slowTTFTUnbindDecision{
		action:       "normal",
		cohort:       cohort,
		firstTokenMs: firstTokenMs,
		percentile:   percentile,
		sampleCount:  sampleCount,
		enabled:      c.cfg.enabled,
	}
	if sampleCount < c.cfg.minSamples {
		decision.action = "insufficient_samples"
		c.emitDecision(decision)
		return decision
	}
	if percentile < c.cfg.observePercentile {
		c.emitDecision(decision)
		return decision
	}
	if percentile < c.cfg.triggerPercentile {
		decision.action = "observe"
		c.emitDecision(decision)
		return decision
	}

	c.samples[sampleIndex].slowCandidate = true
	decision.activeAccounts, decision.badAccounts, decision.globalBadAccountPercent = c.globalBadAccountStatsLocked()
	if !c.cfg.enabled {
		decision.action = "disabled"
		c.emitDecision(decision)
		return decision
	}
	if c.globalProtectionActive(decision) {
		decision.action = "paused"
		c.emitDecision(decision)
		return decision
	}
	if c.affinityCooldownActiveLocked(now, affinityKey) {
		decision.action = "cooldown"
		c.emitDecision(decision)
		return decision
	}

	decision.action = "unbind"
	decision.shouldUnbind = true
	if strings.TrimSpace(affinityKey) != "" {
		c.lastUnbound[affinityKey] = now
	}
	c.emitDecision(decision)
	return decision
}

func (c *slowTTFTUnbindController) now() time.Time {
	if c.timeNow != nil {
		return c.timeNow()
	}
	return time.Now()
}

func (c *slowTTFTUnbindController) pruneLocked(now time.Time) {
	cutoff := now.Add(-c.cfg.window)
	keep := c.samples[:0]
	for _, sample := range c.samples {
		if !sample.at.Before(cutoff) {
			keep = append(keep, sample)
		}
	}
	c.samples = keep
	if c.cfg.affinityCooldown <= 0 {
		clear(c.lastUnbound)
		return
	}
	for key, at := range c.lastUnbound {
		if now.Sub(at) > c.cfg.affinityCooldown {
			delete(c.lastUnbound, key)
		}
	}
}

func (c *slowTTFTUnbindController) percentileRankLocked(cohort string, firstTokenMs int) (float64, int) {
	total := 0
	less := 0
	for _, sample := range c.samples {
		if sample.cohort != cohort {
			continue
		}
		total++
		if sample.firstTokenMs < firstTokenMs {
			less++
		}
	}
	if total == 0 {
		return 0, 0
	}
	return float64(less+1) * 100 / float64(total), total
}

func (c *slowTTFTUnbindController) globalBadAccountStatsLocked() (int, int, float64) {
	active := make(map[int64]struct{})
	bad := make(map[int64]struct{})
	for _, sample := range c.samples {
		if sample.accountID <= 0 {
			continue
		}
		active[sample.accountID] = struct{}{}
		if sample.slowCandidate {
			bad[sample.accountID] = struct{}{}
		}
	}
	activeCount := len(active)
	badCount := len(bad)
	if activeCount == 0 {
		return 0, 0, 0
	}
	return activeCount, badCount, float64(badCount) * 100 / float64(activeCount)
}

func (c *slowTTFTUnbindController) globalProtectionActive(decision slowTTFTUnbindDecision) bool {
	if c.cfg.globalBadAccountPercent <= 0 {
		return false
	}
	if c.cfg.globalMinAccounts > 0 && decision.activeAccounts < c.cfg.globalMinAccounts {
		return false
	}
	return decision.globalBadAccountPercent > c.cfg.globalBadAccountPercent
}

func (c *slowTTFTUnbindController) affinityCooldownActiveLocked(now time.Time, affinityKey string) bool {
	if c.cfg.affinityCooldown <= 0 {
		return false
	}
	affinityKey = strings.TrimSpace(affinityKey)
	if affinityKey == "" {
		return false
	}
	last, ok := c.lastUnbound[affinityKey]
	return ok && now.Sub(last) < c.cfg.affinityCooldown
}

func (c *slowTTFTUnbindController) emitDecision(decision slowTTFTUnbindDecision) {
	if c.decisionHook != nil {
		c.decisionHook(decision)
	}
}

func (d slowTTFTUnbindDecision) shouldLog() bool {
	switch d.action {
	case "observe", "disabled", "paused", "cooldown", "unbind":
		return true
	default:
		return false
	}
}

func (h *Handler) maybeUnbindSlowTTFT(endpoint, model, reasoningEffort string, stream bool, affinityKey string, accountID int64, firstTokenMs int) {
	if h == nil || h.store == nil || h.slowTTFTUnbind == nil || accountID <= 0 || firstTokenMs <= 0 {
		return
	}
	decision := h.slowTTFTUnbind.record(endpoint, model, reasoningEffort, stream, accountID, affinityKey, firstTokenMs)
	if decision.shouldLog() {
		log.Printf("[slow_ttft_unbind] action=%s endpoint=%s model=%s effort=%s stream=%t account=%d first_token_ms=%d percentile=%.2f sample_count=%d active_accounts=%d bad_accounts=%d bad_account_pct=%.2f enabled=%t cohort=%s",
			decision.action, endpoint, model, reasoningEffort, stream, accountID, firstTokenMs, decision.percentile, decision.sampleCount, decision.activeAccounts, decision.badAccounts, decision.globalBadAccountPercent, decision.enabled, decision.cohort)
	}
	if decision.shouldUnbind {
		h.store.UnbindSessionAffinity(affinityKey, accountID)
	}
}
