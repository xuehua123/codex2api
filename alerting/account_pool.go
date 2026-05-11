package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type AccountPoolStore interface {
	AvailableCount() int
	AccountCount() int
}

type AccountPoolDiagnosticProvider interface {
	AccountPoolDiagnostics() AccountPoolDiagnostics
}

type AccountPoolConfig struct {
	Enabled              bool   `json:"enabled"`
	WebhookURL           string `json:"webhook_url"`
	InstanceName         string `json:"instance_name"`
	MinAvailable         int    `json:"min_available"`
	MinAvailableRatio    int    `json:"min_available_ratio"`
	CheckIntervalSeconds int    `json:"check_interval_seconds"`
	ConsecutiveFailures  int    `json:"consecutive_failures"`
	CooldownMinutes      int    `json:"cooldown_minutes"`
	RecoveryBuffer       int    `json:"recovery_buffer"`
	RecoveryRatioBuffer  int    `json:"recovery_ratio_buffer"`
}

type AccountPoolSnapshot struct {
	Available int
	Total     int
	Ratio     int
}

type AccountPoolDiagnostics struct {
	Snapshot        AccountPoolSnapshot
	Issues          []DiagnosticItem
	HealthTiers     []DiagnosticItem
	Plans           []DiagnosticItem
	Recommendations []string
}

type DiagnosticItem struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

type AccountPoolMonitor struct {
	store  AccountPoolStore
	client *http.Client

	mu            sync.Mutex
	cfg           AccountPoolConfig
	alerting      bool
	badCount      int
	lastAlertAt   time.Time
	lastRecoverAt time.Time

	lifecycleMu sync.Mutex
	started     bool
	stopped     bool
	stopCh      chan struct{}
	doneCh      chan struct{}
}

func DefaultAccountPoolConfig() AccountPoolConfig {
	return AccountPoolConfig{
		Enabled:              false,
		MinAvailable:         50,
		MinAvailableRatio:    20,
		CheckIntervalSeconds: 60,
		ConsecutiveFailures:  2,
		CooldownMinutes:      30,
		RecoveryBuffer:       10,
		RecoveryRatioBuffer:  5,
	}
}

func NormalizeAccountPoolConfig(cfg AccountPoolConfig) AccountPoolConfig {
	defaults := DefaultAccountPoolConfig()
	cfg.WebhookURL = strings.TrimSpace(cfg.WebhookURL)
	cfg.InstanceName = strings.TrimSpace(cfg.InstanceName)
	if cfg.MinAvailable < 0 {
		cfg.MinAvailable = defaults.MinAvailable
	}
	if cfg.MinAvailableRatio < 0 || cfg.MinAvailableRatio > 100 {
		cfg.MinAvailableRatio = defaults.MinAvailableRatio
	}
	if cfg.CheckIntervalSeconds < 10 {
		cfg.CheckIntervalSeconds = defaults.CheckIntervalSeconds
	}
	if cfg.CheckIntervalSeconds > 3600 {
		cfg.CheckIntervalSeconds = 3600
	}
	if cfg.ConsecutiveFailures < 1 {
		cfg.ConsecutiveFailures = defaults.ConsecutiveFailures
	}
	if cfg.ConsecutiveFailures > 20 {
		cfg.ConsecutiveFailures = 20
	}
	if cfg.CooldownMinutes < 1 {
		cfg.CooldownMinutes = defaults.CooldownMinutes
	}
	if cfg.CooldownMinutes > 1440 {
		cfg.CooldownMinutes = 1440
	}
	if cfg.RecoveryBuffer < 0 {
		cfg.RecoveryBuffer = defaults.RecoveryBuffer
	}
	if cfg.RecoveryBuffer > 10000 {
		cfg.RecoveryBuffer = 10000
	}
	if cfg.RecoveryRatioBuffer < 0 {
		cfg.RecoveryRatioBuffer = defaults.RecoveryRatioBuffer
	}
	if cfg.RecoveryRatioBuffer > 100 {
		cfg.RecoveryRatioBuffer = 100
	}
	return cfg
}

func AccountPoolConfigFromJSON(raw string) AccountPoolConfig {
	cfg := DefaultAccountPoolConfig()
	if strings.TrimSpace(raw) == "" {
		return cfg
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return DefaultAccountPoolConfig()
	}
	return NormalizeAccountPoolConfig(cfg)
}

func AccountPoolConfigToJSON(cfg AccountPoolConfig) string {
	cfg = NormalizeAccountPoolConfig(cfg)
	data, err := json.Marshal(cfg)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func MaskWebhookURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "****"
	}
	key := u.Query().Get("key")
	if key == "" {
		return strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/") + "?key=****"
	}
	maskedKey := "****"
	if len(key) > 8 {
		maskedKey = key[:4] + "****" + key[len(key)-4:]
	}
	u.RawQuery = "key=" + maskedKey
	return u.String()
}

func IsMaskedWebhookURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return strings.Contains(trimmed, "****") || strings.Contains(strings.ToUpper(trimmed), "%2A%2A%2A%2A")
}

func ValidateWeComWebhookURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme != "https" || u.Hostname() != "qyapi.weixin.qq.com" {
		return errors.New("企业微信 Webhook 必须是 https://qyapi.weixin.qq.com/cgi-bin/webhook/send?... 地址")
	}
	if u.Path != "/cgi-bin/webhook/send" || strings.TrimSpace(u.Query().Get("key")) == "" {
		return errors.New("企业微信 Webhook 缺少有效的 key 参数")
	}
	return nil
}

func NewAccountPoolMonitor(store AccountPoolStore, cfg AccountPoolConfig) *AccountPoolMonitor {
	return &AccountPoolMonitor{
		store:  store,
		client: &http.Client{Timeout: 10 * time.Second},
		cfg:    NormalizeAccountPoolConfig(cfg),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

func (m *AccountPoolMonitor) Start() {
	if m == nil {
		return
	}
	m.lifecycleMu.Lock()
	if m.started || m.stopped {
		m.lifecycleMu.Unlock()
		return
	}
	m.started = true
	m.lifecycleMu.Unlock()
	go m.loop()
}

func (m *AccountPoolMonitor) Stop() {
	if m == nil {
		return
	}
	m.lifecycleMu.Lock()
	if m.stopped {
		m.lifecycleMu.Unlock()
		return
	}
	m.stopped = true
	started := m.started
	close(m.stopCh)
	if !started {
		close(m.doneCh)
		m.lifecycleMu.Unlock()
		return
	}
	m.lifecycleMu.Unlock()
	<-m.doneCh
}

func (m *AccountPoolMonitor) UpdateConfig(cfg AccountPoolConfig) {
	if m == nil {
		return
	}
	nextCfg := NormalizeAccountPoolConfig(cfg)
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := m.cfg != nextCfg
	m.cfg = nextCfg
	if changed || !m.cfg.Enabled {
		m.resetAlertStateLocked()
	}
}

func (m *AccountPoolMonitor) resetAlertStateLocked() {
	m.alerting = false
	m.badCount = 0
	m.lastAlertAt = time.Time{}
	m.lastRecoverAt = time.Time{}
}

func (m *AccountPoolMonitor) Config() AccountPoolConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

func (m *AccountPoolMonitor) loop() {
	defer close(m.doneCh)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-timer.C:
			if err := m.CheckOnce(context.Background()); err != nil {
				log.Printf("账号池告警检查失败: %v", err)
			}
			timer.Reset(m.nextInterval())
		}
	}
}

func (m *AccountPoolMonitor) nextInterval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Duration(m.cfg.CheckIntervalSeconds) * time.Second
}

func (m *AccountPoolMonitor) CheckOnce(ctx context.Context) error {
	if m == nil || m.store == nil {
		return nil
	}
	diagnostics := m.diagnostics()
	snapshot := diagnostics.Snapshot

	m.mu.Lock()
	cfg := m.cfg
	if !cfg.Enabled {
		m.mu.Unlock()
		return nil
	}
	if strings.TrimSpace(cfg.WebhookURL) == "" {
		m.mu.Unlock()
		return nil
	}

	now := time.Now()
	bad := accountPoolIsBad(cfg, snapshot)
	recovered := accountPoolIsRecovered(cfg, snapshot)
	shouldSendAlert := false
	shouldSendRecovery := false
	if bad {
		m.badCount++
		if m.badCount >= cfg.ConsecutiveFailures && (!m.alerting || now.Sub(m.lastAlertAt) >= time.Duration(cfg.CooldownMinutes)*time.Minute) {
			shouldSendAlert = true
		}
	} else if m.alerting && recovered {
		shouldSendRecovery = true
	} else if !m.alerting {
		m.badCount = 0
	}
	m.mu.Unlock()

	if shouldSendAlert {
		if err := SendWeComMarkdown(ctx, m.client, cfg.WebhookURL, renderAccountPoolAlertMarkdown(cfg, diagnostics, now)); err != nil {
			return err
		}
		m.mu.Lock()
		if m.cfg == cfg && m.cfg.Enabled {
			m.alerting = true
			m.lastAlertAt = now
		}
		m.mu.Unlock()
		return nil
	}
	if shouldSendRecovery {
		if err := SendWeComMarkdown(ctx, m.client, cfg.WebhookURL, renderAccountPoolRecoveryMarkdown(cfg, diagnostics, now)); err != nil {
			return err
		}
		m.mu.Lock()
		if m.cfg == cfg && m.cfg.Enabled {
			m.alerting = false
			m.badCount = 0
			m.lastRecoverAt = now
		}
		m.mu.Unlock()
		return nil
	}
	return nil
}

func (m *AccountPoolMonitor) diagnostics() AccountPoolDiagnostics {
	if provider, ok := m.store.(AccountPoolDiagnosticProvider); ok {
		diag := provider.AccountPoolDiagnostics()
		if diag.Snapshot.Total > 0 || diag.Snapshot.Available > 0 {
			return diag
		}
	}
	snapshot := AccountPoolSnapshot{
		Available: m.store.AvailableCount(),
		Total:     m.store.AccountCount(),
	}
	snapshot.Ratio = percent(snapshot.Available, snapshot.Total)
	return AccountPoolDiagnostics{Snapshot: snapshot}
}

func accountPoolIsBad(cfg AccountPoolConfig, s AccountPoolSnapshot) bool {
	if cfg.MinAvailable > 0 && s.Available < cfg.MinAvailable {
		return true
	}
	if cfg.MinAvailableRatio > 0 && s.Ratio < cfg.MinAvailableRatio {
		return true
	}
	return false
}

func accountPoolIsRecovered(cfg AccountPoolConfig, s AccountPoolSnapshot) bool {
	if cfg.MinAvailable > 0 && s.Available < cfg.MinAvailable+cfg.RecoveryBuffer {
		return false
	}
	recoveryRatio := cfg.MinAvailableRatio + cfg.RecoveryRatioBuffer
	if recoveryRatio > 100 {
		recoveryRatio = 100
	}
	if cfg.MinAvailableRatio > 0 && s.Ratio < recoveryRatio {
		return false
	}
	return true
}

func percent(available, total int) int {
	if total <= 0 {
		return 0
	}
	return int(math.Round(float64(available) * 100 / float64(total)))
}

func renderAccountPoolAlertMarkdown(cfg AccountPoolConfig, diagnostics AccountPoolDiagnostics, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## codex2api 账号池告警\n\n")
	appendStatusCardMarkdown(&b, "可用账号不足", "warning", cfg, diagnostics, now, true)
	appendDiagnosticsMarkdown(&b, diagnostics)
	return b.String()
}

func renderAccountPoolRecoveryMarkdown(cfg AccountPoolConfig, diagnostics AccountPoolDiagnostics, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## codex2api 账号池恢复\n\n")
	appendStatusCardMarkdown(&b, "可用账号已恢复", "info", cfg, diagnostics, now, false)
	appendDiagnosticsMarkdown(&b, diagnostics)
	return b.String()
}

func appendStatusCardMarkdown(b *strings.Builder, status, color string, cfg AccountPoolConfig, diagnostics AccountPoolDiagnostics, now time.Time, includeThreshold bool) {
	s := diagnostics.Snapshot
	unavailable := s.Total - s.Available
	if unavailable < 0 {
		unavailable = 0
	}
	fmt.Fprintf(b, "> 实例：%s\n", displayInstanceName(cfg))
	fmt.Fprintf(b, "> 状态：<font color=\"%s\">%s</font>\n", color, status)
	fmt.Fprintf(b, "> 可用：<font color=\"%s\">%d / %d</font>（%d%%）\n", color, s.Available, s.Total, s.Ratio)
	if s.Total > 0 {
		fmt.Fprintf(b, "> 不可用：%d（%d%%）\n", unavailable, percent(unavailable, s.Total))
	}
	if includeThreshold {
		fmt.Fprintf(b, "> 阈值：低于 %s 触发\n", thresholdText(cfg))
	}
	fmt.Fprintf(b, "> 时间：%s\n", now.Format("2006-01-02 15:04:05"))
}

func appendDiagnosticsMarkdown(b *strings.Builder, diagnostics AccountPoolDiagnostics) {
	if len(diagnostics.Issues) > 0 {
		appendDiagnosticSectionMarkdown(b, "主要原因", diagnostics.Issues, diagnostics.Snapshot.Total, 5)
	}
	if len(diagnostics.HealthTiers) > 0 {
		appendDiagnosticSectionMarkdown(b, "健康层级", diagnostics.HealthTiers, diagnostics.Snapshot.Total, 0)
	}
	if len(diagnostics.Plans) > 0 {
		appendDiagnosticSectionMarkdown(b, "套餐分布", diagnostics.Plans, diagnostics.Snapshot.Total, 5)
	}
	if len(diagnostics.Recommendations) > 0 {
		fmt.Fprintf(b, "\n**建议动作**\n")
		for i, item := range diagnostics.Recommendations {
			fmt.Fprintf(b, "> %d. %s\n", i+1, item)
		}
	}
}

func appendDiagnosticSectionMarkdown(b *strings.Builder, title string, items []DiagnosticItem, total, limit int) {
	fmt.Fprintf(b, "\n**%s**\n", title)
	for _, item := range topDiagnosticItems(items, limit) {
		fmt.Fprintf(b, "> - %s：%d%s\n", item.Label, item.Count, diagnosticPercentText(item.Count, total))
	}
}

func diagnosticPercentText(count, total int) string {
	if total <= 0 || count <= 0 {
		return ""
	}
	return fmt.Sprintf("（%d%%）", percent(count, total))
}

func thresholdText(cfg AccountPoolConfig) string {
	parts := make([]string, 0, 2)
	if cfg.MinAvailable > 0 {
		parts = append(parts, fmt.Sprintf("%d 个", cfg.MinAvailable))
	}
	if cfg.MinAvailableRatio > 0 {
		parts = append(parts, fmt.Sprintf("%d%%", cfg.MinAvailableRatio))
	}
	if len(parts) == 0 {
		return "未配置阈值"
	}
	return strings.Join(parts, " 或 ")
}

func topDiagnosticItems(items []DiagnosticItem, limit int) []DiagnosticItem {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[:limit]
}

func displayInstanceName(cfg AccountPoolConfig) string {
	if strings.TrimSpace(cfg.InstanceName) != "" {
		return strings.TrimSpace(cfg.InstanceName)
	}
	return "codex2api"
}

func SendWeComMarkdown(ctx context.Context, client *http.Client, webhookURL, content string) error {
	if err := ValidateWeComWebhookURL(webhookURL); err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	payload := map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"content": content,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("企业微信 Webhook HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var decoded struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(respBody, &decoded); err == nil && decoded.ErrCode != 0 {
		return fmt.Errorf("企业微信 Webhook 返回错误 %d: %s", decoded.ErrCode, decoded.ErrMsg)
	}
	return nil
}

func RenderTestMarkdown(cfg AccountPoolConfig, available, total int, now time.Time) string {
	snapshot := AccountPoolSnapshot{Available: available, Total: total, Ratio: percent(available, total)}
	return RenderDiagnosticTestMarkdown(cfg, AccountPoolDiagnostics{Snapshot: snapshot}, now)
}

func RenderDiagnosticTestMarkdown(cfg AccountPoolConfig, diagnostics AccountPoolDiagnostics, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## codex2api 告警测试\n\n")
	appendStatusCardMarkdown(&b, "测试通知", "info", cfg, diagnostics, now, true)
	appendDiagnosticsMarkdown(&b, diagnostics)
	return b.String()
}
