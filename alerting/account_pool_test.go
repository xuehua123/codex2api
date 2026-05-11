package alerting

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMaskWebhookURLKeepsOnlyKeyEdges(t *testing.T) {
	raw := "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=1234567890abcdef"
	got := MaskWebhookURL(raw)
	if got == raw {
		t.Fatal("masked URL should not equal raw URL")
	}
	if !IsMaskedWebhookURL(got) {
		t.Fatalf("masked URL = %q, want marker", got)
	}
	if got != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=1234****cdef" {
		t.Fatalf("masked URL = %q", got)
	}
}

func TestValidateWeComWebhookURLRejectsNonWeComTargets(t *testing.T) {
	cases := []string{
		"http://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc",
		"https://example.com/cgi-bin/webhook/send?key=abc",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send",
		"https://qyapi.weixin.qq.com/other?key=abc",
	}
	for _, tc := range cases {
		if err := ValidateWeComWebhookURL(tc); err == nil {
			t.Fatalf("ValidateWeComWebhookURL(%q) = nil, want error", tc)
		}
	}
}

func TestAccountPoolThresholdAndRecoveryBuffer(t *testing.T) {
	cfg := NormalizeAccountPoolConfig(AccountPoolConfig{
		MinAvailable:        50,
		MinAvailableRatio:   20,
		RecoveryBuffer:      10,
		RecoveryRatioBuffer: 5,
	})

	if !accountPoolIsBad(cfg, AccountPoolSnapshot{Available: 49, Total: 200, Ratio: 25}) {
		t.Fatal("count below threshold should be bad")
	}
	if !accountPoolIsBad(cfg, AccountPoolSnapshot{Available: 80, Total: 500, Ratio: 16}) {
		t.Fatal("ratio below threshold should be bad")
	}
	if accountPoolIsRecovered(cfg, AccountPoolSnapshot{Available: 55, Total: 200, Ratio: 28}) {
		t.Fatal("available below recovery buffer should not recover")
	}
	if !accountPoolIsRecovered(cfg, AccountPoolSnapshot{Available: 60, Total: 200, Ratio: 30}) {
		t.Fatal("snapshot above recovery thresholds should recover")
	}
}

func TestRenderDiagnosticTestMarkdownIncludesCausesAndRecommendations(t *testing.T) {
	cfg := AccountPoolConfig{InstanceName: "new-stable", MinAvailable: 50, MinAvailableRatio: 20}
	content := RenderDiagnosticTestMarkdown(cfg, AccountPoolDiagnostics{
		Snapshot: AccountPoolSnapshot{Available: 42, Total: 200, Ratio: 21},
		Issues: []DiagnosticItem{
			{Key: "rate_limited_5h", Label: "5h 限流", Count: 80},
			{Key: "error", Label: "错误状态", Count: 12},
		},
		HealthTiers: []DiagnosticItem{
			{Key: "healthy", Label: "healthy", Count: 42},
			{Key: "risky", Label: "risky", Count: 90},
		},
		Recommendations: []string{"等待 5h 窗口重置。"},
	}, testNow())

	for _, want := range []string{"new-stable", "测试通知", "可用：<font color=\"info\">42 / 200</font>（21%）", "不可用：158（79%）", "阈值：低于 50 个 或 20% 触发", "主要原因", "5h 限流：80（40%）", "健康层级", "risky：90（45%）", "建议动作", "1. 等待 5h 窗口重置"} {
		if !contains(content, want) {
			t.Fatalf("content missing %q:\n%s", want, content)
		}
	}
}

func TestCheckOnceRetriesAlertAfterSendFailure(t *testing.T) {
	store := &fakeDiagnosticStore{diagnostics: AccountPoolDiagnostics{
		Snapshot: AccountPoolSnapshot{Available: 0, Total: 10, Ratio: 0},
	}}
	monitor := NewAccountPoolMonitor(store, testAlertConfig())
	calls := 0
	monitor.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("webhook unavailable")
		}
		return okWeComResponse(), nil
	})}

	if err := monitor.CheckOnce(context.Background()); err == nil {
		t.Fatal("first CheckOnce error = nil, want webhook error")
	}
	if calls != 1 {
		t.Fatalf("webhook calls = %d, want 1", calls)
	}
	if monitorIsAlerting(monitor) {
		t.Fatal("monitor should not enter alerting state after failed send")
	}

	if err := monitor.CheckOnce(context.Background()); err != nil {
		t.Fatalf("second CheckOnce error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("webhook calls = %d, want retry on second check", calls)
	}
	if !monitorIsAlerting(monitor) {
		t.Fatal("monitor should enter alerting state after successful send")
	}
}

func TestCheckOnceKeepsAlertingWhenRecoverySendFails(t *testing.T) {
	store := &fakeDiagnosticStore{diagnostics: AccountPoolDiagnostics{
		Snapshot: AccountPoolSnapshot{Available: 0, Total: 10, Ratio: 0},
	}}
	monitor := NewAccountPoolMonitor(store, testAlertConfig())
	calls := 0
	monitor.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("recovery webhook unavailable")
		}
		return okWeComResponse(), nil
	})}

	if err := monitor.CheckOnce(context.Background()); err != nil {
		t.Fatalf("alert CheckOnce error = %v", err)
	}
	if !monitorIsAlerting(monitor) {
		t.Fatal("monitor should enter alerting state after alert send")
	}

	store.diagnostics.Snapshot = AccountPoolSnapshot{Available: 2, Total: 10, Ratio: 20}
	if err := monitor.CheckOnce(context.Background()); err == nil {
		t.Fatal("recovery CheckOnce error = nil, want webhook error")
	}
	if !monitorIsAlerting(monitor) {
		t.Fatal("monitor should stay alerting when recovery send fails")
	}

	if err := monitor.CheckOnce(context.Background()); err != nil {
		t.Fatalf("recovery retry CheckOnce error = %v", err)
	}
	if calls != 3 {
		t.Fatalf("webhook calls = %d, want alert plus failed and retried recovery", calls)
	}
	if monitorIsAlerting(monitor) {
		t.Fatal("monitor should clear alerting state after successful recovery send")
	}
}

func TestUpdateConfigResetsAlertStateWhenConfigChanges(t *testing.T) {
	store := &fakeDiagnosticStore{diagnostics: AccountPoolDiagnostics{
		Snapshot: AccountPoolSnapshot{Available: 0, Total: 10, Ratio: 0},
	}}
	monitor := NewAccountPoolMonitor(store, testAlertConfig())
	monitor.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return okWeComResponse(), nil
	})}

	if err := monitor.CheckOnce(context.Background()); err != nil {
		t.Fatalf("CheckOnce error = %v", err)
	}
	if !monitorIsAlerting(monitor) {
		t.Fatal("monitor should enter alerting state before config change")
	}

	nextCfg := testAlertConfig()
	nextCfg.WebhookURL = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=fedcba0987654321"
	monitor.UpdateConfig(nextCfg)

	if monitorIsAlerting(monitor) {
		t.Fatal("monitor should reset alerting state after config change")
	}
	if badCount := monitorBadCount(monitor); badCount != 0 {
		t.Fatalf("badCount = %d, want 0 after config change", badCount)
	}
}

func TestMonitorStartStopAreIdempotent(t *testing.T) {
	store := &fakeDiagnosticStore{diagnostics: AccountPoolDiagnostics{
		Snapshot: AccountPoolSnapshot{Available: 10, Total: 10, Ratio: 100},
	}}
	monitor := NewAccountPoolMonitor(store, testAlertConfig())
	monitor.Start()
	monitor.Start()
	monitor.Stop()
	monitor.Stop()

	stoppedBeforeStart := NewAccountPoolMonitor(store, testAlertConfig())
	stoppedBeforeStart.Stop()
	stoppedBeforeStart.Start()
	stoppedBeforeStart.Stop()
}

func testNow() time.Time {
	return time.Date(2026, 5, 11, 12, 30, 0, 0, time.Local)
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func testAlertConfig() AccountPoolConfig {
	return AccountPoolConfig{
		Enabled:             true,
		WebhookURL:          "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=1234567890abcdef",
		MinAvailable:        1,
		ConsecutiveFailures: 1,
		CooldownMinutes:     30,
		RecoveryBuffer:      0,
	}
}

func monitorIsAlerting(m *AccountPoolMonitor) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.alerting
}

func monitorBadCount(m *AccountPoolMonitor) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.badCount
}

type fakeDiagnosticStore struct {
	diagnostics AccountPoolDiagnostics
}

func (s *fakeDiagnosticStore) AccountPoolDiagnostics() AccountPoolDiagnostics {
	return s.diagnostics
}

func (s *fakeDiagnosticStore) AvailableCount() int {
	return s.diagnostics.Snapshot.Available
}

func (s *fakeDiagnosticStore) AccountCount() int {
	return s.diagnostics.Snapshot.Total
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func okWeComResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"errcode":0,"errmsg":"ok"}`)),
		Header:     make(http.Header),
	}
}
