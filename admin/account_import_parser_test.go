package admin

import "testing"

func TestParseImportTokenLineSupportsPlainRefreshToken(t *testing.T) {
	line := "rt_plain_refresh_token"

	token, ok := parseImportTokenLine(line)
	if !ok {
		t.Fatalf("expected plain refresh token to be accepted")
	}
	if token.refreshToken != line {
		t.Fatalf("refreshToken = %q, want %q", token.refreshToken, line)
	}
	if token.name != "" {
		t.Fatalf("name = %q, want empty", token.name)
	}
}

func TestParseImportTokenLineSupportsCPAOriginalFormat(t *testing.T) {
	line := "benkautzervvg@outlook.com----rt_cFE4WxhqqX84MYvBMdZxT1oYxLDe7ee3-1WhWUCSXb8.2GTy5FhniPSZ6E1bFF4Za8BtCz7tQTqo6mdfnZM7O2E----eyJhbGciOiJSUzI1NiJ9----0f56cc84-9059-4c8c-bfc9-d0ba42db351b"

	token, ok := parseImportTokenLine(line)
	if !ok {
		t.Fatalf("expected CPA original format to be accepted")
	}
	if got, want := token.refreshToken, "rt_cFE4WxhqqX84MYvBMdZxT1oYxLDe7ee3-1WhWUCSXb8.2GTy5FhniPSZ6E1bFF4Za8BtCz7tQTqo6mdfnZM7O2E"; got != want {
		t.Fatalf("refreshToken = %q, want %q", got, want)
	}
	if got, want := token.name, "benkautzervvg@outlook.com"; got != want {
		t.Fatalf("name = %q, want %q", got, want)
	}
}

func TestParseImportTokenLinesSkipsInvalidRowsAndDeduplicatesByRefreshToken(t *testing.T) {
	lines := []string{
		"\ufefffirst@example.com----rt_first----jwt----uuid",
		"rt_second",
		"invalid-row-without-refresh-token",
		"another@example.com----rt_first----jwt----uuid-2",
		"   ",
	}

	tokens := parseImportTokenLines(lines)
	if got, want := len(tokens), 2; got != want {
		t.Fatalf("len(tokens) = %d, want %d", got, want)
	}

	if got, want := tokens[0].name, "first@example.com"; got != want {
		t.Fatalf("tokens[0].name = %q, want %q", got, want)
	}
	if got, want := tokens[0].refreshToken, "rt_first"; got != want {
		t.Fatalf("tokens[0].refreshToken = %q, want %q", got, want)
	}
	if got, want := tokens[1].refreshToken, "rt_second"; got != want {
		t.Fatalf("tokens[1].refreshToken = %q, want %q", got, want)
	}
}
