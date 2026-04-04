package database

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestNewSQLiteInitializesFreshDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")

	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	if got := db.Driver(); got != "sqlite" {
		t.Fatalf("Driver() = %q, want %q", got, "sqlite")
	}
}

func TestListProxiesReturnsBoundAccounts(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")

	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	if _, err := db.InsertProxy(ctx, "socks5://10.0.0.2:10001", "slot-1"); err != nil {
		t.Fatalf("InsertProxy(slot-1) 返回错误: %v", err)
	}
	if _, err := db.InsertProxy(ctx, "socks5://10.0.0.2:10002", "slot-2"); err != nil {
		t.Fatalf("InsertProxy(slot-2) 返回错误: %v", err)
	}

	if _, err := db.InsertAccount(ctx, "a1", "rt-1", "socks5://10.0.0.2:10001"); err != nil {
		t.Fatalf("InsertAccount(a1) 返回错误: %v", err)
	}
	if _, err := db.InsertAccount(ctx, "a2", "rt-2", "socks5://10.0.0.2:10001"); err != nil {
		t.Fatalf("InsertAccount(a2) 返回错误: %v", err)
	}
	if _, err := db.InsertAccount(ctx, "a3", "rt-3", ""); err != nil {
		t.Fatalf("InsertAccount(a3) 返回错误: %v", err)
	}

	proxies, err := db.ListProxies(ctx)
	if err != nil {
		t.Fatalf("ListProxies 返回错误: %v", err)
	}
	if len(proxies) != 2 {
		t.Fatalf("ListProxies 返回 %d 条, want 2", len(proxies))
	}

	if proxies[0].URL != "socks5://10.0.0.2:10001" || proxies[0].BoundAccounts != 2 {
		t.Fatalf("proxy[0] = (%q, %d), want (%q, %d)", proxies[0].URL, proxies[0].BoundAccounts, "socks5://10.0.0.2:10001", 2)
	}
	if proxies[1].URL != "socks5://10.0.0.2:10002" || proxies[1].BoundAccounts != 0 {
		t.Fatalf("proxy[1] = (%q, %d), want (%q, %d)", proxies[1].URL, proxies[1].BoundAccounts, "socks5://10.0.0.2:10002", 0)
	}
}

func TestSQLiteUsageLogsHasAPIKeyColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")

	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	columns, err := db.sqliteTableColumns(context.Background(), "usage_logs")
	if err != nil {
		t.Fatalf("sqliteTableColumns 返回错误: %v", err)
	}

	for _, name := range []string{"api_key_id", "api_key_name", "api_key_masked"} {
		if _, ok := columns[name]; !ok {
			t.Fatalf("usage_logs 缺少列 %q", name)
		}
	}
}

func TestUsageLogsFilterByAPIKeyID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")

	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	now := time.Now().UTC()
	targetAPIKeyID := int64(7)

	logs := []*UsageLogInput{
		{
			AccountID:    1,
			Endpoint:     "/v1/chat/completions",
			Model:        "gpt-5.4",
			StatusCode:   200,
			DurationMs:   120,
			APIKeyID:     targetAPIKeyID,
			APIKeyName:   "Team A",
			APIKeyMasked: "sk-a****...****1111",
		},
		{
			AccountID:    1,
			Endpoint:     "/v1/responses",
			Model:        "gpt-5.4",
			StatusCode:   200,
			DurationMs:   220,
			APIKeyID:     targetAPIKeyID,
			APIKeyName:   "Team A",
			APIKeyMasked: "sk-a****...****1111",
		},
		{
			AccountID:    2,
			Endpoint:     "/v1/responses",
			Model:        "gpt-5.4-mini",
			StatusCode:   200,
			DurationMs:   320,
			APIKeyID:     8,
			APIKeyName:   "Team B",
			APIKeyMasked: "sk-b****...****2222",
		},
	}

	for _, usageLog := range logs {
		if err := db.InsertUsageLog(ctx, usageLog); err != nil {
			t.Fatalf("InsertUsageLog 返回错误: %v", err)
		}
	}
	db.flushLogs()

	recentLogs, err := db.ListRecentUsageLogs(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecentUsageLogs 返回错误: %v", err)
	}
	if len(recentLogs) != len(logs) {
		t.Fatalf("recentLogs 长度 = %d, want %d", len(recentLogs), len(logs))
	}

	foundSnapshot := false
	for _, usageLog := range recentLogs {
		if usageLog.APIKeyID == targetAPIKeyID {
			foundSnapshot = true
			if usageLog.APIKeyName != "Team A" {
				t.Fatalf("APIKeyName = %q, want %q", usageLog.APIKeyName, "Team A")
			}
			if usageLog.APIKeyMasked != "sk-a****...****1111" {
				t.Fatalf("APIKeyMasked = %q, want %q", usageLog.APIKeyMasked, "sk-a****...****1111")
			}
		}
	}
	if !foundSnapshot {
		t.Fatal("未找到带 API 密钥快照的最近日志")
	}

	page, err := db.ListUsageLogsByTimeRangePaged(ctx, UsageLogFilter{
		Start:    now.Add(-1 * time.Hour),
		End:      now.Add(1 * time.Hour),
		Page:     1,
		PageSize: 10,
		APIKeyID: &targetAPIKeyID,
	})
	if err != nil {
		t.Fatalf("ListUsageLogsByTimeRangePaged 返回错误: %v", err)
	}

	if page.Total != 2 {
		t.Fatalf("page.Total = %d, want %d", page.Total, 2)
	}
	if len(page.Logs) != 2 {
		t.Fatalf("len(page.Logs) = %d, want %d", len(page.Logs), 2)
	}
	for _, usageLog := range page.Logs {
		if usageLog.APIKeyID != targetAPIKeyID {
			t.Fatalf("APIKeyID = %d, want %d", usageLog.APIKeyID, targetAPIKeyID)
		}
		if usageLog.APIKeyName != "Team A" {
			t.Fatalf("APIKeyName = %q, want %q", usageLog.APIKeyName, "Team A")
		}
	}
}

func TestUpdateAccountsProxyUpdatesOnlySelectedAccounts(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")

	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	id1, err := db.InsertAccount(ctx, "a1", "rt-1", "socks5://10.0.0.2:10001")
	if err != nil {
		t.Fatalf("InsertAccount(a1) 返回错误: %v", err)
	}
	id2, err := db.InsertAccount(ctx, "a2", "rt-2", "")
	if err != nil {
		t.Fatalf("InsertAccount(a2) 返回错误: %v", err)
	}
	id3, err := db.InsertAccount(ctx, "a3", "rt-3", "socks5://10.0.0.2:10003")
	if err != nil {
		t.Fatalf("InsertAccount(a3) 返回错误: %v", err)
	}

	updated, err := db.UpdateAccountsProxy(ctx, []int64{id1, id3}, "socks5://10.0.0.2:10005")
	if err != nil {
		t.Fatalf("UpdateAccountsProxy 返回错误: %v", err)
	}
	if updated != 2 {
		t.Fatalf("UpdateAccountsProxy 更新 %d 条, want 2", updated)
	}

	accounts, err := db.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts 返回错误: %v", err)
	}

	got := map[int64]string{}
	for _, account := range accounts {
		got[account.ID] = account.ProxyURL
	}

	if got[id1] != "socks5://10.0.0.2:10005" {
		t.Fatalf("account %d proxy = %q, want %q", id1, got[id1], "socks5://10.0.0.2:10005")
	}
	if got[id2] != "" {
		t.Fatalf("account %d proxy = %q, want empty", id2, got[id2])
	}
	if got[id3] != "socks5://10.0.0.2:10005" {
		t.Fatalf("account %d proxy = %q, want %q", id3, got[id3], "socks5://10.0.0.2:10005")
	}
}
