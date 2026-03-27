package admin

import "strings"

func parseImportTokenLine(line string) (importToken, bool) {
	line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
	if line == "" {
		return importToken{}, false
	}

	if strings.HasPrefix(line, "rt_") {
		return importToken{refreshToken: line}, true
	}

	parts := strings.Split(line, "----")
	if len(parts) < 2 {
		return importToken{}, false
	}

	name := strings.TrimSpace(parts[0])
	for _, raw := range parts {
		part := strings.TrimSpace(raw)
		if strings.HasPrefix(part, "rt_") {
			return importToken{
				refreshToken: part,
				name:         name,
			}, true
		}
	}

	return importToken{}, false
}

func parseImportTokenLines(lines []string) []importToken {
	seen := make(map[string]bool)
	tokens := make([]importToken, 0, len(lines))

	for _, line := range lines {
		token, ok := parseImportTokenLine(line)
		if !ok || token.refreshToken == "" || seen[token.refreshToken] {
			continue
		}
		seen[token.refreshToken] = true
		tokens = append(tokens, token)
	}

	return tokens
}
