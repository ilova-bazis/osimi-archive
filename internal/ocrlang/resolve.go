package ocrlang

import (
	"fmt"
	"strings"
	"unicode"
)

var aliases = map[string]string{
	"en":  "eng",
	"eng": "eng",
	"ru":  "rus",
	"rus": "rus",
	"tg":  "tgk",
	"tgk": "tgk",
	"tj":  "tgk",
	"tjk": "tgk",
}

func Resolve(preferred, fallback string) (string, error) {
	if strings.TrimSpace(preferred) != "" {
		return Normalize(preferred)
	}
	if strings.TrimSpace(fallback) != "" {
		return Normalize(fallback)
	}
	return "eng", nil
}

func Normalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("ocr language is empty")
	}

	parts := strings.Split(raw, "+")
	resolved := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		lang, err := normalizeToken(part)
		if err != nil {
			return "", err
		}
		if _, ok := seen[lang]; ok {
			continue
		}
		seen[lang] = struct{}{}
		resolved = append(resolved, lang)
	}
	if len(resolved) == 0 {
		return "", fmt.Errorf("ocr language is empty")
	}
	return strings.Join(resolved, "+"), nil
}

func normalizeToken(token string) (string, error) {
	token = strings.TrimSpace(strings.ToLower(token))
	if token == "" {
		return "", fmt.Errorf("ocr language contains an empty token")
	}
	if idx := strings.IndexAny(token, "-_"); idx > 0 {
		token = token[:idx]
	}
	if mapped, ok := aliases[token]; ok {
		return mapped, nil
	}
	for _, r := range token {
		if !unicode.IsLetter(r) || r > unicode.MaxASCII {
			return "", fmt.Errorf("unsupported ocr language %q", token)
		}
	}
	if len(token) >= 3 {
		return token, nil
	}
	return "", fmt.Errorf("unsupported ocr language %q", token)
}
