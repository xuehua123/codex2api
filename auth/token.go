package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// OpenAI OAuth 常量（与 CLIProxyAPI / sub2api 一致）
const (
	TokenURL      = "https://auth.openai.com/oauth/token"
	ClientID      = "app_EMoamEEZ73f0CkXaXp7hrann"
	RefreshScopes = "openid profile email"
	MaxRetries    = 3
)

// TokenData 保存一次 RT 刷新获得的 token 信息
type TokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
	ExpiresAt    time.Time
}

// AccountInfo 解析 id_token 获得的账号信息
type AccountInfo struct {
	Email            string `json:"email"`
	ChatGPTAccountID string `json:"chatgpt_account_id"`
	PlanType         string `json:"chatgpt_plan_type"`
}

// RefreshAccessToken 用 RT 或 Session Token 换取 AT
func RefreshAccessToken(ctx context.Context, tokenStr string, proxyURL string) (*TokenData, *AccountInfo, error) {
	// 如果是 eyJ 开头，说明是 session_token，使用新的 Session API
	if strings.HasPrefix(tokenStr, "eyJ") {
		return RefreshBySessionToken(ctx, tokenStr, proxyURL)
	}

	data := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {ClientID},
		"refresh_token": {tokenStr},
		"scope":         {RefreshScopes},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := buildHTTPClient(proxyURL)
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("刷新请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("刷新失败 (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, nil, fmt.Errorf("解析响应失败: %w", err)
	}

	td := &TokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		IDToken:      tokenResp.IDToken,
		ExpiresIn:    tokenResp.ExpiresIn,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
	}

	// 保留新 RT，如果返回空则保留旧的
	if strings.TrimSpace(td.RefreshToken) == "" {
		td.RefreshToken = tokenStr
	}

	// 解析 id_token 获取账号信息
	info := parseIDToken(tokenResp.IDToken)

	return td, info, nil
}

// RefreshWithRetry 带重试的 RT 刷新
func RefreshWithRetry(ctx context.Context, refreshToken string, proxyURL string) (*TokenData, *AccountInfo, error) {
	var lastErr error
	for attempt := 0; attempt < MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		td, info, err := RefreshAccessToken(ctx, refreshToken, proxyURL)
		if err == nil {
			return td, info, nil
		}

		// 不可重试错误直接返回
		if isNonRetryable(err) {
			return nil, nil, err
		}
		lastErr = err
	}
	return nil, nil, fmt.Errorf("刷新失败（重试 %d 次）: %w", MaxRetries, lastErr)
}

// isNonRetryable 判断是否不可重试的认证错误
func isNonRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{"invalid_grant", "invalid_client", "unauthorized_client", "access_denied"} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// parseIDToken 解析 JWT id_token 的 payload（不验签）
func parseIDToken(idToken string) *AccountInfo {
	if idToken == "" {
		return &AccountInfo{}
	}

	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return &AccountInfo{}
	}

	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}

	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return &AccountInfo{}
		}
	}

	var claims struct {
		Email      string `json:"email"`
		OpenAIAuth *struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
			PlanType         string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
		Profile *struct {
			Email string `json:"email"`
		} `json:"https://api.openai.com/profile"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return &AccountInfo{}
	}

	info := &AccountInfo{Email: claims.Email}
	if info.Email == "" && claims.Profile != nil && claims.Profile.Email != "" {
		info.Email = claims.Profile.Email
	}
	if claims.OpenAIAuth != nil {
		info.ChatGPTAccountID = claims.OpenAIAuth.ChatGPTAccountID
		info.PlanType = claims.OpenAIAuth.PlanType
	}
	return info
}

// RefreshBySessionToken 使用 Session Token (eyJ...) 在 API 换取 AT
func RefreshBySessionToken(ctx context.Context, sessionToken string, proxyURL string) (*TokenData, *AccountInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/api/auth/session", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("创建会话请求失败: %w", err)
	}

	req.AddCookie(&http.Cookie{
		Name:  "__Secure-next-auth.session-token",
		Value: sessionToken,
	})
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	client := buildHTTPClient(proxyURL)
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("会话刷新请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return nil, nil, fmt.Errorf("%s", "invalid_grant: "+string(body))
		}
		return nil, nil, fmt.Errorf("会话刷新失败 (status %d): %s", resp.StatusCode, string(body))
	}

	var sessionResp struct {
		AccessToken  string `json:"accessToken"`
		SessionToken string `json:"sessionToken"`
		Expires      string `json:"expires"`
	}
	if err := json.Unmarshal(body, &sessionResp); err != nil {
		return nil, nil, fmt.Errorf("解析会话响应失败: %w", err)
	}

	if sessionResp.AccessToken == "" {
		if strings.TrimSpace(string(body)) == "{}" {
			// next-auth session expired/invalid returns {}
			return nil, nil, fmt.Errorf("invalid_grant: session token invalid or expired")
		}
		return nil, nil, fmt.Errorf("解析会话响应失败: accessToken 为空 (body: %s)", string(body)[:min(100, len(body))])
	}

	td := &TokenData{
		AccessToken:  sessionResp.AccessToken,
		RefreshToken: sessionToken,
	}

	// 从响应中如果获得了新的 Session Token 则进行替换
	if strings.HasPrefix(sessionResp.SessionToken, "eyJ") {
		td.RefreshToken = sessionResp.SessionToken
	}

	// 尽量解析过期时间
	expiresAt, err := time.Parse(time.RFC3339, sessionResp.Expires)
	if err == nil {
		td.ExpiresAt = expiresAt
		td.ExpiresIn = int64(time.Until(expiresAt).Seconds())
	} else {
		td.ExpiresIn = 3600
		td.ExpiresAt = time.Now().Add(1 * time.Hour)
	}

	// 从 access_token 中解析出账户邮箱等信息
	info := parseIDToken(sessionResp.AccessToken)

	return td, info, nil
}

// AccessTokenInfo AT JWT 解析结果
type AccessTokenInfo struct {
	Email            string
	ChatGPTAccountID string
	PlanType         string
	ExpiresAt        time.Time
}

// ParseAccessToken 解析 Access Token 的 JWT payload（不验签）
// AT 的 email 在 https://api.openai.com/profile 下，与 id_token 不同
func ParseAccessToken(accessToken string) *AccessTokenInfo {
	if accessToken == "" {
		return nil
	}

	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return nil
	}

	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}

	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil
		}
	}

	var claims struct {
		Exp        int64 `json:"exp"`
		OpenAIAuth *struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
			PlanType         string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
		OpenAIProfile *struct {
			Email string `json:"email"`
		} `json:"https://api.openai.com/profile"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil
	}

	info := &AccessTokenInfo{}
	if claims.OpenAIProfile != nil {
		info.Email = claims.OpenAIProfile.Email
	}
	if claims.OpenAIAuth != nil {
		info.ChatGPTAccountID = claims.OpenAIAuth.ChatGPTAccountID
		info.PlanType = claims.OpenAIAuth.PlanType
	}
	if claims.Exp > 0 {
		info.ExpiresAt = time.Unix(claims.Exp, 0)
	}
	return info
}

// authClientPool 认证请求的连接池（按 proxyURL 分组，带 TTL 清理）
var authClientPool sync.Map // map[string]*authPoolEntry

type authPoolEntry struct {
	client   *http.Client
	lastUsed atomic.Int64
}

func (e *authPoolEntry) touch() {
	e.lastUsed.Store(time.Now().UnixNano())
}

const (
	authClientPoolTTL             = 5 * time.Minute
	authClientPoolCleanupInterval = 60 * time.Second
)

// authClientPoolStop 用于停止清理协程（测试中可调用以避免 goroutine 泄漏）
var authClientPoolStop = make(chan struct{})

func init() {
	go func() {
		ticker := time.NewTicker(authClientPoolCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				evictExpiredAuthClients()
			case <-authClientPoolStop:
				return
			}
		}
	}()
}

func evictExpiredAuthClients() {
	cutoff := time.Now().Add(-authClientPoolTTL).UnixNano()
	authClientPool.Range(func(key, value any) bool {
		entry := value.(*authPoolEntry)
		if entry.lastUsed.Load() < cutoff {
			authClientPool.Delete(key)
			entry.client.CloseIdleConnections()
		}
		return true
	})
}

// buildHTTPClient 构建支持代理的 HTTP 客户端（连接池复用，带 TTL 清理）
func buildHTTPClient(proxyURL string) *http.Client {
	if v, ok := authClientPool.Load(proxyURL); ok {
		entry := v.(*authPoolEntry)
		entry.touch()
		return entry.client
	}

	transport := &http.Transport{
		MaxIdleConns:        20,
		MaxIdleConnsPerHost: 10,
		MaxConnsPerHost:     20,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	baseDialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = baseDialer.DialContext
	if proxyURL != "" {
		if err := ConfigureTransportProxy(transport, proxyURL, baseDialer); err != nil {
			transport.Proxy = nil
			transport.DialContext = baseDialer.DialContext
		}
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	entry := &authPoolEntry{client: client}
	entry.touch()

	if v, loaded := authClientPool.LoadOrStore(proxyURL, entry); loaded {
		e := v.(*authPoolEntry)
		e.touch()
		return e.client
	}
	return client
}

// BuildHTTPClient builds a proxy-aware HTTP client (exported for admin OAuth flow).
func BuildHTTPClient(proxyURL string) *http.Client {
	return buildHTTPClient(proxyURL)
}

// ParseIDToken parses a JWT id_token payload (exported for admin OAuth flow).
func ParseIDToken(idToken string) *AccountInfo {
	return parseIDToken(idToken)
}

// HashAccountID 从 account_id 生成短哈希（用于日志）
func HashAccountID(accountID string) string {
	if accountID == "" {
		return ""
	}
	h := sha256.Sum256([]byte(accountID))
	return fmt.Sprintf("%x", h[:4])
}
