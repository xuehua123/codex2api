package database

import (
	"context"
	"path/filepath"
	"testing"
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
