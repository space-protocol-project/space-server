// Одноразовый loopback TURN для сквозной проверки настольного клиента.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/space-protocol-project/space-server/internal/voice/turnfixture"
)

func run() error {
	if os.Getenv("SPACE_VOICE_TURN_CHECK") != "1" || len(os.Args) != 2 {
		return fmt.Errorf("Только изолированный тестовый режим")
	}
	root, err := filepath.Abs(os.Args[1])
	if err != nil || !regexp.MustCompile(`^voice-check-[a-f0-9]{32}$`).MatchString(filepath.Base(root)) {
		return fmt.Errorf("Нужен каталог тестового стенда")
	}
	address, err := privateAddress()
	if err != nil {
		return err
	}
	fixture, err := turnfixture.StartOn(address)
	if err != nil {
		return err
	}
	defer fixture.Close()
	data, err := json.Marshal(map[string]string{"udp": fixture.UDP, "tcp": fixture.TCP, "secret": fixture.Secret})
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(root, "turn.json"), data, 0600); err != nil {
		return err
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(10 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-deadline.C:
			return fmt.Errorf("Время тестового TURN истекло")
		case <-ticker.C:
			if _, err = os.Stat(filepath.Join(root, "turn.stop")); err == nil {
				return nil
			}
		}
	}
}

func privateAddress() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, item := range interfaces {
		if item.Flags&net.FlagUp == 0 || item.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := item.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && ip.IsPrivate() {
				return ip.String(), nil
			}
		}
	}
	return "", fmt.Errorf("Для нативной проверки TURN нужен частный IPv4 интерфейс")
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
