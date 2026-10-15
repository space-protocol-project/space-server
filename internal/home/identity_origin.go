package home

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// Адрес в подписях остаётся постоянным после явно подтверждённого переноса.
func IdentityOrigin(data, fallback string) (string, error) {
	bytes, err := os.ReadFile(filepath.Join(filepath.Dir(data), "config.json"))
	if os.IsNotExist(err) {
		return fallback, nil
	}
	if err != nil {
		return "", err
	}
	var config struct {
		Origin string `json:"identity_origin"`
	}
	if err := json.Unmarshal(bytes, &config); err != nil {
		return "", err
	}
	if config.Origin == "" {
		return fallback, nil
	}
	parsed, err := url.Parse(config.Origin)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("Повреждён постоянный адрес пространства")
	}
	return config.Origin, nil
}
