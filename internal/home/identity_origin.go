package home

import (
	"encoding/json"
	"fmt"
	"net"
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
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Port() != "8443" || net.ParseIP(parsed.Hostname()).To4() == nil {
		return "", fmt.Errorf("Повреждён постоянный адрес пространства")
	}
	return config.Origin, nil
}
