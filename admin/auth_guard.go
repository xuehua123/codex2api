package admin

import (
	"context"
	"sync"
	"time"
)

type authGuardConfig struct {
	now            func() time.Time
	sleep          func(time.Duration)
	blockDuration  time.Duration
	tarpitDuration time.Duration
	queueTimeout   time.Duration
	maxConcurrent  int
}

type authGuard struct {
	mu             sync.Mutex
	ipFailures     map[string]int
	ipBlocks       map[string]time.Time
	authSemaphore  chan struct{}
	now            func() time.Time
	sleep          func(time.Duration)
	blockDuration  time.Duration
	tarpitDuration time.Duration
	queueTimeout   time.Duration
}

func newAuthGuard(cfg authGuardConfig) *authGuard {
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.sleep == nil {
		cfg.sleep = time.Sleep
	}
	if cfg.blockDuration <= 0 {
		cfg.blockDuration = 5 * time.Minute
	}
	if cfg.tarpitDuration < 0 {
		cfg.tarpitDuration = 0
	}
	if cfg.queueTimeout <= 0 {
		cfg.queueTimeout = time.Second
	}
	if cfg.maxConcurrent <= 0 {
		cfg.maxConcurrent = 5
	}

	return &authGuard{
		ipFailures:     make(map[string]int),
		ipBlocks:       make(map[string]time.Time),
		authSemaphore:  make(chan struct{}, cfg.maxConcurrent),
		now:            cfg.now,
		sleep:          cfg.sleep,
		blockDuration:  cfg.blockDuration,
		tarpitDuration: cfg.tarpitDuration,
		queueTimeout:   cfg.queueTimeout,
	}
}

func (g *authGuard) IsBlocked(clientIP string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	blockUntil, ok := g.ipBlocks[clientIP]
	if !ok {
		return false
	}
	if g.now().Before(blockUntil) {
		return true
	}

	delete(g.ipBlocks, clientIP)
	delete(g.ipFailures, clientIP)
	return false
}

func (g *authGuard) Acquire(ctx context.Context) (func(), bool) {
	timer := time.NewTimer(g.queueTimeout)
	defer timer.Stop()

	select {
	case g.authSemaphore <- struct{}{}:
		return func() { <-g.authSemaphore }, true
	case <-timer.C:
		return nil, false
	case <-ctx.Done():
		return nil, false
	}
}

func (g *authGuard) RegisterFailure(clientIP string) {
	g.mu.Lock()
	g.ipFailures[clientIP]++
	if g.ipFailures[clientIP] >= 5 {
		g.ipBlocks[clientIP] = g.now().Add(g.blockDuration)
	}
	g.mu.Unlock()

	if g.tarpitDuration > 0 {
		g.sleep(g.tarpitDuration)
	}
}

func (g *authGuard) RecordSuccess(clientIP string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	delete(g.ipFailures, clientIP)
	delete(g.ipBlocks, clientIP)
}
