package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

// Handler 管理后台 API 处理器
type Handler struct {
	store       *auth.Store
	db          *database.DB
	rateLimiter *proxy.RateLimiter
}

// NewHandler 创建管理后台处理器
func NewHandler(store *auth.Store, db *database.DB, rl *proxy.RateLimiter) *Handler {
	return &Handler{store: store, db: db, rateLimiter: rl}
}

// RegisterRoutes 注册管理 API 路由
func (h *Handler) RegisterRoutes(r *gin.Engine) {
	api := r.Group("/api/admin")
	api.GET("/stats", h.GetStats)
	api.GET("/accounts", h.ListAccounts)
	api.POST("/accounts", h.AddAccount)
	api.DELETE("/accounts/:id", h.DeleteAccount)
	api.POST("/accounts/:id/refresh", h.RefreshAccount)
	api.GET("/usage/stats", h.GetUsageStats)
	api.GET("/usage/logs", h.GetUsageLogs)
	api.DELETE("/usage/logs", h.ClearUsageLogs)
	api.GET("/keys", h.ListAPIKeys)
	api.POST("/keys", h.CreateAPIKey)
	api.DELETE("/keys/:id", h.DeleteAPIKey)
	api.GET("/health", h.GetHealth)
	api.GET("/settings", h.GetSettings)
	api.PUT("/settings", h.UpdateSettings)
}

// ==================== Stats ====================

// GetStats 获取仪表盘统计
func (h *Handler) GetStats(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	accounts, err := h.db.ListActive(ctx)
	if err != nil {
		writeInternalError(c, err)
		return
	}

	total := len(accounts)
	available := h.store.AvailableCount()
	errCount := 0
	for _, acc := range accounts {
		if acc.Status == "error" {
			errCount++
		}
	}

	usageStats, _ := h.db.GetUsageStats(ctx)
	todayReqs := int64(0)
	if usageStats != nil {
		todayReqs = usageStats.TodayRequests
	}

	c.JSON(http.StatusOK, statsResponse{
		Total:         total,
		Available:     available,
		Error:         errCount,
		TodayRequests: todayReqs,
	})
}

// ==================== Accounts ====================

type accountResponse struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Email          string `json:"email"`
	PlanType       string `json:"plan_type"`
	Status         string `json:"status"`
	ProxyURL       string `json:"proxy_url"`
	UpdatedAt      string `json:"updated_at"`
	ActiveRequests int64  `json:"active_requests"`
	TotalRequests  int64  `json:"total_requests"`
	LastUsedAt     string `json:"last_used_at"`
}

// ListAccounts 获取账号列表
func (h *Handler) ListAccounts(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.ListActive(ctx)
	if err != nil {
		writeInternalError(c, err)
		return
	}

	// 合并内存中的调度指标
	accountMap := make(map[int64]*auth.Account)
	for _, acc := range h.store.Accounts() {
		accountMap[acc.DBID] = acc
	}

	accounts := make([]accountResponse, 0, len(rows))
	for _, row := range rows {
		resp := accountResponse{
			ID:        row.ID,
			Name:      row.Name,
			Email:     row.GetCredential("email"),
			PlanType:  row.GetCredential("plan_type"),
			Status:    row.Status,
			ProxyURL:  row.ProxyURL,
			UpdatedAt: row.UpdatedAt.Format(time.RFC3339),
		}
		if acc, ok := accountMap[row.ID]; ok {
			resp.ActiveRequests = acc.GetActiveRequests()
			resp.TotalRequests = acc.GetTotalRequests()
			if t := acc.GetLastUsedAt(); !t.IsZero() {
				resp.LastUsedAt = t.Format(time.RFC3339)
			}
		}
		accounts = append(accounts, resp)
	}

	c.JSON(http.StatusOK, accountsResponse{Accounts: accounts})
}

type addAccountReq struct {
	Name         string `json:"name"`
	RefreshToken string `json:"refresh_token"`
	ProxyURL     string `json:"proxy_url"`
}

// AddAccount 添加新账号（支持批量：refresh_token 按行分割）
func (h *Handler) AddAccount(c *gin.Context) {
	var req addAccountReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "请求格式错误")
		return
	}

	if req.RefreshToken == "" {
		writeError(c, http.StatusBadRequest, "refresh_token 是必填字段")
		return
	}

	// 按行分割，支持批量添加
	lines := strings.Split(req.RefreshToken, "\n")
	var tokens []string
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t != "" {
			tokens = append(tokens, t)
		}
	}

	if len(tokens) == 0 {
		writeError(c, http.StatusBadRequest, "未找到有效的 Refresh Token")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	successCount := 0
	failCount := 0

	for i, rt := range tokens {
		name := req.Name
		if name == "" {
			name = fmt.Sprintf("account-%d", i+1)
		} else if len(tokens) > 1 {
			name = fmt.Sprintf("%s-%d", req.Name, i+1)
		}

		id, err := h.db.InsertAccount(ctx, name, rt, req.ProxyURL)
		if err != nil {
			log.Printf("批量添加账号 %d 失败: %v", i+1, err)
			failCount++
			continue
		}

		successCount++

		// 热加载：直接加入内存池
		newAcc := &auth.Account{
			DBID:         id,
			RefreshToken: rt,
			ProxyURL:     req.ProxyURL,
		}
		h.store.AddAccount(newAcc)

		// 异步刷新 AT
		go func(accountID int64) {
			refreshCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := h.store.RefreshSingle(refreshCtx, accountID); err != nil {
				log.Printf("新账号 %d 刷新失败: %v", accountID, err)
			} else {
				log.Printf("新账号 %d 刷新成功，已加入号池", accountID)
			}
		}(id)
	}

	msg := fmt.Sprintf("成功添加 %d 个账号", successCount)
	if failCount > 0 {
		msg += fmt.Sprintf("，%d 个失败", failCount)
	}

	c.JSON(http.StatusOK, gin.H{
		"message": msg,
		"success": successCount,
		"failed":  failCount,
	})
}

// DeleteAccount 删除账号
func (h *Handler) DeleteAccount(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "无效的账号 ID")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	// 标记为 deleted 而非物理删除
	if err := h.db.SetError(ctx, id, "deleted"); err != nil {
		writeError(c, http.StatusInternalServerError, "删除失败: "+err.Error())
		return
	}

	// 从内存池移除
	h.store.RemoveAccount(id)

	writeMessage(c, http.StatusOK, "账号已删除")
}

// RefreshAccount 手动刷新账号 AT
func (h *Handler) RefreshAccount(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "无效的账号 ID")
		return
	}

	// 查找运行时账号并触发刷新
	_ = id // TODO: 实现通过 ID 查找运行时 Account 并触发刷新

	writeMessage(c, http.StatusOK, "刷新请求已发送")
}

// ==================== Health ====================

// GetHealth 系统健康检查（扩展版）
func (h *Handler) GetHealth(c *gin.Context) {
	c.JSON(http.StatusOK, healthResponse{
		Status:    "ok",
		Available: h.store.AvailableCount(),
		Total:     h.store.AccountCount(),
	})
}

// ==================== Usage ====================

// GetUsageStats 获取使用统计
func (h *Handler) GetUsageStats(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	stats, err := h.db.GetUsageStats(ctx)
	if err != nil {
		writeInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, stats)
}

// GetUsageLogs 获取使用日志
func (h *Handler) GetUsageLogs(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	limit := 50
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	logs, err := h.db.ListRecentUsageLogs(ctx, limit)
	if err != nil {
		writeInternalError(c, err)
		return
	}
	if logs == nil {
		logs = []*database.UsageLog{}
	}
	c.JSON(http.StatusOK, usageLogsResponse{Logs: logs})
}

// ClearUsageLogs 清空所有使用日志
func (h *Handler) ClearUsageLogs(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	if err := h.db.ClearUsageLogs(ctx); err != nil {
		writeInternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "日志已清空"})
}

// ==================== API Keys ====================

// ListAPIKeys 获取所有 API 密钥
func (h *Handler) ListAPIKeys(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	keys, err := h.db.ListAPIKeys(ctx)
	if err != nil {
		writeInternalError(c, err)
		return
	}
	if keys == nil {
		keys = []*database.APIKeyRow{}
	}
	c.JSON(http.StatusOK, apiKeysResponse{Keys: keys})
}

type createKeyReq struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// generateKey 生成随机 API Key
func generateKey() string {
	b := make([]byte, 24)
	rand.Read(b)
	return "sk-" + hex.EncodeToString(b)
}

// CreateAPIKey 创建新 API 密钥
func (h *Handler) CreateAPIKey(c *gin.Context) {
	var req createKeyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		req.Name = ""
	}
	if req.Name == "" {
		req.Name = "default"
	}

	key := req.Key
	if key == "" {
		key = generateKey()
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	id, err := h.db.InsertAPIKey(ctx, req.Name, key)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "创建失败: "+err.Error())
		return
	}

	c.JSON(http.StatusOK, createAPIKeyResponse{
		ID:   id,
		Key:  key,
		Name: req.Name,
	})
}

// DeleteAPIKey 删除 API 密钥
func (h *Handler) DeleteAPIKey(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "无效 ID")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()

	if err := h.db.DeleteAPIKey(ctx, id); err != nil {
		writeError(c, http.StatusInternalServerError, "删除失败: "+err.Error())
		return
	}
	writeMessage(c, http.StatusOK, "已删除")
}

// ==================== Settings ====================

type settingsResponse struct {
	MaxConcurrency int `json:"max_concurrency"`
	GlobalRPM      int `json:"global_rpm"`
}

type updateSettingsReq struct {
	MaxConcurrency *int `json:"max_concurrency"`
	GlobalRPM      *int `json:"global_rpm"`
}

// GetSettings 获取当前系统设置
func (h *Handler) GetSettings(c *gin.Context) {
	c.JSON(http.StatusOK, settingsResponse{
		MaxConcurrency: h.store.GetMaxConcurrency(),
		GlobalRPM:      h.rateLimiter.GetRPM(),
	})
}

// UpdateSettings 更新系统设置（实时生效）
func (h *Handler) UpdateSettings(c *gin.Context) {
	var req updateSettingsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "请求格式错误")
		return
	}

	if req.MaxConcurrency != nil {
		v := *req.MaxConcurrency
		if v < 1 {
			v = 1
		}
		if v > 50 {
			v = 50
		}
		h.store.SetMaxConcurrency(v)
		log.Printf("设置已更新: max_concurrency = %d", v)
	}

	if req.GlobalRPM != nil {
		v := *req.GlobalRPM
		if v < 0 {
			v = 0
		}
		h.rateLimiter.UpdateRPM(v)
		log.Printf("设置已更新: global_rpm = %d", v)
	}

	c.JSON(http.StatusOK, settingsResponse{
		MaxConcurrency: h.store.GetMaxConcurrency(),
		GlobalRPM:      h.rateLimiter.GetRPM(),
	})
}
