package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/codex2api/auth"
)

// WhamUsageURL 是 ChatGPT 后端用量查询端点。
// 该端点返回结构化 JSON（不消耗任何额度），可用于零成本获取账号 5h/7d 用量。
const WhamUsageURL = "https://chatgpt.com/backend-api/wham/usage"

// whamURLForTest 允许测试替换默认 URL。生产代码不要赋值。
var whamURLForTest = ""

// WhamUsage 是 /backend-api/wham/usage 的响应结构。
type WhamUsage struct {
	UserID    string `json:"user_id"`
	AccountID string `json:"account_id"`
	Email     string `json:"email"`
	PlanType  string `json:"plan_type"`

	RateLimit struct {
		Allowed         bool             `json:"allowed"`
		LimitReached    bool             `json:"limit_reached"`
		PrimaryWindow   *WhamUsageWindow `json:"primary_window"`
		SecondaryWindow *WhamUsageWindow `json:"secondary_window"`
	} `json:"rate_limit"`

	Credits *struct {
		HasCredits           bool   `json:"has_credits"`
		Unlimited            bool   `json:"unlimited"`
		OverageLimitReached  bool   `json:"overage_limit_reached"`
		Balance              string `json:"balance"`
		ApproxLocalMessages  []int  `json:"approx_local_messages"`
		ApproxCloudMessages  []int  `json:"approx_cloud_messages"`
	} `json:"credits,omitempty"`

	SpendControl *struct {
		Reached         bool        `json:"reached"`
		IndividualLimit interface{} `json:"individual_limit"`
	} `json:"spend_control,omitempty"`
}

// WhamUsageWindow 是单个限流窗口（primary=5h，secondary=7d）。
type WhamUsageWindow struct {
	UsedPercent         float64 `json:"used_percent"`
	LimitWindowSeconds  int64   `json:"limit_window_seconds"`
	ResetAfterSeconds   int64   `json:"reset_after_seconds"`
	ResetAt             int64   `json:"reset_at"`
}

// QueryWhamUsage 调用 /backend-api/wham/usage 获取账号当前用量。
// 该调用不消耗任何 token 额度——比发送最小 /responses 请求更便宜。
func QueryWhamUsage(ctx context.Context, account *auth.Account, proxyURL string) (*WhamUsage, *http.Response, error) {
	url := WhamUsageURL
	if whamURLForTest != "" {
		url = whamURLForTest
	}
	return queryWhamUsageWithURL(ctx, account, proxyURL, url)
}

func queryWhamUsageWithURL(ctx context.Context, account *auth.Account, proxyURL, url string) (*WhamUsage, *http.Response, error) {
	if account == nil {
		return nil, nil, fmt.Errorf("account is nil")
	}
	accessToken := account.GetAccessToken()
	if accessToken == "" {
		return nil, nil, fmt.Errorf("account has no access token")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("build wham request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", latestCodexCLIUserAgentPrefix)
	req.Header.Set("Originator", Originator)
	if accountID := strings.TrimSpace(account.AccountID); accountID != "" {
		req.Header.Set("chatgpt-account-id", accountID)
	}

	client := &http.Client{Transport: newCodexStandardTransport(proxyURL)}

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("wham request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// 调用方需要根据状态码触发刷新 / 冷却；返回 resp 让上层处理 body。
		return nil, resp, fmt.Errorf("wham returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if err != nil {
		return nil, resp, fmt.Errorf("read wham response: %w", err)
	}

	var usage WhamUsage
	if err := json.Unmarshal(body, &usage); err != nil {
		return nil, resp, fmt.Errorf("parse wham response: %w", err)
	}
	return &usage, resp, nil
}

// ApplyWhamUsage 将 /wham/usage 返回的数据写入账号 state + 持久化。
// 行为与 SyncCodexUsageState（处理 /responses 响应头时）保持一致：
//   - plan_type 同步到内存 + DB
//   - 5h 窗口写入 SetUsageSnapshot5h
//   - 7d 窗口走 PersistUsageSnapshot
//   - premium 5h 用尽时走 MarkPremium5hRateLimited
func ApplyWhamUsage(store *auth.Store, account *auth.Account, usage *WhamUsage) CodexUsageSyncResult {
	result := CodexUsageSyncResult{}
	if account == nil || usage == nil {
		return result
	}

	if store != nil && usage.PlanType != "" {
		store.UpdateAccountPlanType(account, usage.PlanType)
	}

	now := time.Now()

	// 5h 主窗口
	if w := usage.RateLimit.PrimaryWindow; w != nil && w.LimitWindowSeconds > 0 {
		resetAt := whamWindowResetAt(w, now)
		account.SetUsageSnapshot5h(w.UsedPercent, resetAt)
		result.UsagePct5h = w.UsedPercent
		result.Reset5hAt = resetAt
		result.HasUsage5h = true
		result.Used5hHeaders = true
	}

	// 7d 次窗口
	if w := usage.RateLimit.SecondaryWindow; w != nil && w.LimitWindowSeconds > 0 {
		resetAt := whamWindowResetAt(w, now)
		account.SetReset7dAt(resetAt)
		result.UsagePct7d = w.UsedPercent
		result.HasUsage7d = true
		if store != nil {
			store.PersistUsageSnapshot(account, w.UsedPercent)
		}
	} else if result.Used5hHeaders && store != nil {
		// 只有 5h 数据时，单独持久化 5h 快照
		store.PersistUsageSnapshot5hOnly(account)
		result.Persisted5hOnly = true
	}

	// premium 5h 限流标记
	if result.Used5hHeaders && account.IsPremium5hPlan() && result.HasUsage5h && result.UsagePct5h >= 100 {
		if store != nil {
			store.MarkPremium5hRateLimited(account, result.Reset5hAt)
		}
		result.Premium5hRateLimited = true
	}

	return result
}

func whamWindowResetAt(w *WhamUsageWindow, now time.Time) time.Time {
	if w == nil {
		return time.Time{}
	}
	// reset_at 是 unix 时间戳（秒），优先使用；缺失时 fallback 到 reset_after_seconds
	if w.ResetAt > 0 {
		return time.Unix(w.ResetAt, 0)
	}
	if w.ResetAfterSeconds > 0 {
		return now.Add(time.Duration(w.ResetAfterSeconds) * time.Second)
	}
	return time.Time{}
}
