package auth

import (
	"encoding/base64"
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
