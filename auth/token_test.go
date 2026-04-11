package auth

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseIDTokenFallsBackToProfileEmail(t *testing.T) {
	payload := `{"https://api.openai.com/profile":{"email":"profile@example.com"}}`
	token := "header." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"

	info := parseIDToken(token)

	if info.Email != "profile@example.com" {
		t.Fatalf("parseIDToken() email = %q, want %q", info.Email, "profile@example.com")
	}
}

func TestRefreshAccessTokenPreservesOriginalRefreshTokenAndSendsItInRequest(t *testing.T) {
	const wantRefreshToken = "rt-original"

	oldDecorator := ResinRequestDecorator
	t.Cleanup(func() {
		ResinRequestDecorator = oldDecorator
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		if got := r.Form.Get("refresh_token"); got != wantRefreshToken {
			t.Fatalf("refresh_token form value = %q, want %q", got, wantRefreshToken)
		}
		if got := r.Header.Get("X-Resin-Account"); got != "acct-1" {
			t.Fatalf("X-Resin-Account = %q, want %q", got, "acct-1")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"access-token","id_token":"","expires_in":60}`)
	}))
	defer server.Close()

	ResinRequestDecorator = func(targetURL, accountID string) string {
		if targetURL != TokenURL {
			t.Fatalf("targetURL = %q, want %q", targetURL, TokenURL)
		}
		if accountID != "acct-1" {
			t.Fatalf("accountID = %q, want %q", accountID, "acct-1")
		}
		return server.URL
	}

	td, _, err := RefreshAccessToken(context.Background(), wantRefreshToken, "", "acct-1")
	if err != nil {
		t.Fatalf("RefreshAccessToken() error = %v", err)
	}
	if td.RefreshToken != wantRefreshToken {
		t.Fatalf("RefreshToken = %q, want %q", td.RefreshToken, wantRefreshToken)
	}
	if !strings.HasPrefix(td.AccessToken, "access-token") {
		t.Fatalf("AccessToken = %q, want prefix %q", td.AccessToken, "access-token")
	}
}
