package voice

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // TURN REST требует HMAC-SHA1, это не хранение паролей.
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/webrtc/v4"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
)

// NetworkConfig задаётся владельцем runtime, не входящим RPC.
type NetworkConfig struct {
	STUNURLs, TURNURLs []string
	TURNSecret         []byte
	RelayOnly          bool
	PublicIP           string
	UDPMin, UDPMax     uint16
}

func NetworkFromEnvironment() (NetworkConfig, error) {
	c := NetworkConfig{STUNURLs: splitURLs(os.Getenv("SPACE_VOICE_STUN_URLS")), TURNURLs: splitURLs(os.Getenv("SPACE_VOICE_TURN_URLS")), PublicIP: os.Getenv("SPACE_VOICE_PUBLIC_IP")}
	if value := os.Getenv("SPACE_VOICE_RELAY_ONLY"); value != "" {
		if value != "true" && value != "false" {
			return c, fmt.Errorf("SPACE_VOICE_RELAY_ONLY: нужны true или false")
		}
		c.RelayOnly = value == "true"
	}
	secret, file := os.Getenv("SPACE_VOICE_TURN_SECRET"), os.Getenv("SPACE_VOICE_TURN_SECRET_FILE")
	if secret != "" && file != "" {
		return c, fmt.Errorf("Укажите только один источник TURN-секрета")
	}
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return c, fmt.Errorf("Не удалось прочитать файл TURN-секрета")
		}
		secret = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	}
	c.TURNSecret = []byte(secret)
	var err error
	if c.UDPMin, err = envPort("SPACE_VOICE_UDP_MIN"); err != nil {
		return c, err
	}
	if c.UDPMax, err = envPort("SPACE_VOICE_UDP_MAX"); err != nil {
		return c, err
	}
	return c, c.validate()
}

func splitURLs(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func envPort(name string) (uint16, error) {
	value := os.Getenv(name)
	if value == "" {
		return 0, nil
	}
	number, err := strconv.ParseUint(value, 10, 16)
	if err != nil || number == 0 {
		return 0, fmt.Errorf("%s: нужен порт 1–65535", name)
	}
	return uint16(number), nil
}

func (c NetworkConfig) validate() error {
	if err := validateICEURLs(c.STUNURLs, false); err != nil {
		return err
	}
	if err := validateICEURLs(c.TURNURLs, true); err != nil {
		return err
	}
	if len(c.TURNURLs) > 0 && (len(c.TURNSecret) < 32 || len(c.TURNSecret) > 256) {
		return fmt.Errorf("TURN требует общий секрет длиной 32–256 байт")
	}
	if strings.ContainsAny(string(c.TURNSecret), "\r\n\x00") {
		return fmt.Errorf("TURN-секрет не должен содержать переводы строк или NUL")
	}
	if len(c.TURNURLs) == 0 && (len(c.TURNSecret) > 0 || c.RelayOnly) {
		return fmt.Errorf("Для TURN-секрета или relay-only нужны TURN URL")
	}
	if c.PublicIP != "" && (net.ParseIP(c.PublicIP) == nil || !net.ParseIP(c.PublicIP).IsGlobalUnicast()) {
		return fmt.Errorf("SPACE_VOICE_PUBLIC_IP: нужен IP, не имя хоста")
	}
	if (c.UDPMin == 0) != (c.UDPMax == 0) || c.UDPMax < c.UDPMin {
		return fmt.Errorf("Нужен полный упорядоченный диапазон UDP портов")
	}
	return nil
}

func validateICEURLs(urls []string, turn bool) error {
	if len(urls) > 8 {
		return fmt.Errorf("Допускается до восьми STUN и восьми TURN URL")
	}
	for _, raw := range urls {
		uri, err := stun.ParseURI(raw)
		if err != nil || len(raw) > 256 {
			return fmt.Errorf("Некорректный URL сервера ICE")
		}
		isTURN := uri.Scheme == stun.SchemeTypeTURN || uri.Scheme == stun.SchemeTypeTURNS
		if isTURN != turn || (!turn && uri.Scheme != stun.SchemeTypeSTUN) || strings.ContainsAny(raw, "\r\n\t @") {
			return fmt.Errorf("Несоответствие типа или формата URL сервера ICE")
		}
	}
	return nil
}

func (c NetworkConfig) settings() (webrtc.SettingEngine, error) {
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	if err := c.validate(); err != nil {
		return settings, err
	}
	if c.UDPMin != 0 {
		if err := settings.SetEphemeralUDPPortRange(c.UDPMin, c.UDPMax); err != nil {
			return settings, err
		}
	}
	if c.PublicIP != "" {
		settings.SetNAT1To1IPs([]string{c.PublicIP}, webrtc.ICECandidateTypeSrflx)
	}
	return settings, nil
}

func (c NetworkConfig) credentials() (*pb.GetVoiceConfigurationResponse, error) {
	result := &pb.GetVoiceConfigurationResponse{RelayOnly: c.RelayOnly}
	if len(c.STUNURLs) > 0 {
		result.IceServers = append(result.IceServers, &pb.VoiceIceServer{Urls: append([]string(nil), c.STUNURLs...)})
	}
	if len(c.TURNURLs) == 0 {
		return result, nil
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	result.ExpiresAtUnix = time.Now().Add(24 * time.Hour).Unix()
	username := strconv.FormatInt(result.ExpiresAtUnix, 10) + ":" + hex.EncodeToString(nonce[:])
	mac := hmac.New(sha1.New, c.TURNSecret)
	_, _ = mac.Write([]byte(username))
	password := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	result.IceServers = append(result.IceServers, &pb.VoiceIceServer{Urls: append([]string(nil), c.TURNURLs...), Username: username, Credential: password})
	return result, nil
}

func (c NetworkConfig) peerConfiguration() (webrtc.Configuration, error) {
	issued, err := c.credentials()
	if err != nil {
		return webrtc.Configuration{}, err
	}
	result := webrtc.Configuration{}
	for _, entry := range issued.IceServers {
		// Явный внешний IP уже задаёт srflx. Pion запрещает одновременно
		// собирать его через STUN; клиент при этом продолжает использовать STUN.
		if c.PublicIP != "" && entry.Username == "" {
			continue
		}
		result.ICEServers = append(result.ICEServers, webrtc.ICEServer{URLs: entry.Urls, Username: entry.Username, Credential: entry.Credential})
	}
	if c.RelayOnly {
		result.ICETransportPolicy = webrtc.ICETransportPolicyRelay
	}
	return result, nil
}

func (s *Service) GetVoiceConfiguration(ctx context.Context, req *pb.GetVoiceConfigurationRequest) (*pb.GetVoiceConfigurationResponse, error) {
	if _, err := s.identify(ctx, req.ChannelId); err != nil {
		return nil, err
	}
	return s.network.credentials()
}
