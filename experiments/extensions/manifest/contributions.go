package manifest

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Contribution связывает элемент интерфейса только с объявленным query.
type Contribution struct {
	ID    string            `json:"id"`
	Kind  string            `json:"kind"`
	Title map[string]string `json:"title"`
	Query string            `json:"query"`
}

var languageID = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

func contributionFields(raw []byte) bool {
	var entries []map[string]json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || entries == nil || len(entries) > 32 {
		return false
	}
	for _, fields := range entries {
		if !exactContributionFields(fields) {
			return false
		}
	}
	return true
}

func exactContributionFields(fields map[string]json.RawMessage) bool {
	if len(fields) != 4 {
		return false
	}
	for _, name := range []string{"id", "kind", "title", "query"} {
		value, exists := fields[name]
		if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	return true
}

func validContributions(items []Contribution, queries map[string]string) bool {
	if len(items) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, item := range items {
		if seen[item.ID] || !validContribution(item, queries) {
			return false
		}
		seen[item.ID] = true
	}
	return true
}

func validContribution(item Contribution, queries map[string]string) bool {
	if !operationID.MatchString(item.ID) || queries[item.Query] == "" {
		return false
	}
	if item.Kind != "overview" && item.Kind != "section" {
		return false
	}
	if len(item.Title) == 0 || len(item.Title) > 8 || item.Title["en"] == "" {
		return false
	}
	for language, text := range item.Title {
		if !languageID.MatchString(language) || !validTitle(text) {
			return false
		}
	}
	return true
}

func validTitle(text string) bool {
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 120 {
		return false
	}
	for _, char := range text {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}
