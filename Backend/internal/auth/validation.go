package auth

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var steamIDPattern = regexp.MustCompile(`^[0-9]{16,20}$`)

func ValidateSteamID(value string) error {
	if !steamIDPattern.MatchString(value) {
		return errors.New("steam_id must contain 16 to 20 decimal digits")
	}
	return nil
}

func NormalizePersonaName(value, fallback string) (string, error) {
	if !utf8.ValidString(value) {
		return "", errors.New("persona_name must be valid UTF-8")
	}
	value = strings.TrimSpace(value)
	filtered := make([]rune, 0, len([]rune(value)))
	for _, current := range value {
		if unicode.IsControl(current) {
			continue
		}
		filtered = append(filtered, current)
		if len(filtered) == 64 {
			break
		}
	}
	normalized := strings.TrimSpace(string(filtered))
	if normalized == "" {
		normalized = strings.TrimSpace(fallback)
	}
	if normalized == "" {
		normalized = "Player"
	}
	return normalized, nil
}
