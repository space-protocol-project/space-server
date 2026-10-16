package home

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type DoctorCheck struct {
	Code   string `json:"code"`
	Status string `json:"status"`
	Target string `json:"target,omitempty"`
}

type DoctorReport struct {
	Format            int           `json:"format"`
	Checks            []DoctorCheck `json:"checks"`
	Endpoints         []Endpoint    `json:"endpoints"`
	Fingerprint       string        `json:"fingerprint,omitempty"`
	ServerID          string        `json:"server_id,omitempty"`
	External          string        `json:"external"`
	ExternalCheckedAt int64         `json:"external_checked_at_unix_ms,omitempty"`
}

type DoctorOptions struct {
	Data        string
	Endpoints   []Endpoint
	Fingerprint string
	ServerID    string
	WANIP       string
}

// Диагностика только читает конфигурацию и выполняет ограниченные TLS/discovery запросы.
func Diagnose(ctx context.Context, options DoctorOptions) (DoctorReport, error) {
	report := DoctorReport{Format: 1, Checks: []DoctorCheck{}, Endpoints: options.Endpoints,
		Fingerprint: options.Fingerprint, ServerID: options.ServerID, External: "unverified"}
	if options.Data != "" {
		if err := prepareLocalDoctor(ctx, options.Data, &report); err != nil {
			return report, err
		}
	}
	if err := ValidateEndpoints(report.Endpoints); err != nil {
		return report, err
	}
	if options.Data == "" && (!validDoctorPin(report.Fingerprint) || !doctorServerID.MatchString(report.ServerID)) {
		return report, fmt.Errorf("Нужны SHA-256 сертификата и server ID из карточки пространства")
	}
	appendWANCheck(&report, options.WANIP)
	appendAutomaticChecks(&report, options.Data, time.Now())
	for _, endpoint := range report.Endpoints {
		if ctx.Err() != nil {
			report.add("budget_exhausted", "unknown", "")
			break
		}
		report.add(probeDoctorEndpoint(ctx, endpoint, "", report.Fingerprint, report.ServerID), "", endpoint.Origin())
	}
	appendExternalWitness(&report, options.Data, time.Now())
	return report, nil
}

func (report *DoctorReport) add(code, status, target string) {
	if status == "" {
		status = "failed"
		if code == "route_ok" {
			status = "ok"
		}
		if strings.TrimPrefix(code, "local_") == "not_ready" {
			status = "warning"
		}
	}
	report.Checks = append(report.Checks, DoctorCheck{Code: code, Status: status, Target: target})
}

func prepareLocalDoctor(ctx context.Context, data string, report *DoctorReport) error {
	config, err := ReadNetworkConfig(data)
	if err != nil {
		return err
	}
	report.Endpoints = config.AdvertisedEndpoints()
	if len(report.Endpoints) == 0 {
		if config.IP == "" {
			return fmt.Errorf("Адреса пространства ещё не настроены")
		}
		report.Endpoints = []Endpoint{{Host: config.IP, Port: config.Port, Type: "https"}}
	}
	if err := ValidateEndpoints(report.Endpoints); err != nil {
		return err
	}
	if !readDoctorIdentity(data, report) {
		return nil
	}
	addresses := localDoctorAddresses(config.ListenHost, config.ListenPort)
	if len(addresses) == 0 {
		report.add("local_interface_missing", "failed", config.ListenHost)
	}
	for _, address := range addresses {
		code := probeDoctorEndpoint(ctx, report.Endpoints[0], address, report.Fingerprint, report.ServerID)
		if code == "route_ok" {
			code = "local_ok"
			report.add(code, "ok", address)
		} else {
			report.add("local_"+code, "", address)
		}
	}
	return nil
}

func readDoctorIdentity(data string, report *DoctorReport) bool {
	certificate, err := readDoctorCertificate(filepath.Join(data, "server.crt"))
	if err != nil {
		report.add("certificate_missing", "failed", "")
		return false
	}
	digest := sha256.Sum256(certificate.Raw)
	report.Fingerprint = hex.EncodeToString(digest[:])
	if time.Now().Before(certificate.NotBefore) || time.Now().After(certificate.NotAfter) {
		report.add("certificate_expired", "failed", "")
	} else {
		report.add("certificate_ok", "ok", "")
	}
	// В state.json есть секрет первого входа. Он не попадает в отчёт.
	var state struct {
		ServerID string `json:"server_id"`
	}
	if content, err := readDoctorFile(filepath.Join(data, "state.json")); err == nil {
		_ = json.Unmarshal(content, &state)
	}
	if doctorServerID.MatchString(state.ServerID) {
		report.ServerID = state.ServerID
	}
	return true
}

func localDoctorAddresses(host string, port int) []string {
	if port == 0 {
		port = 8443
	}
	switch host {
	case "::":
		return []string{net.JoinHostPort("::1", fmt.Sprint(port)), net.JoinHostPort("127.0.0.1", fmt.Sprint(port))}
	case "", "0.0.0.0":
		return []string{net.JoinHostPort("127.0.0.1", fmt.Sprint(port))}
	default:
		if !doctorLocalIP(host) {
			return nil
		}
		return []string{net.JoinHostPort(host, fmt.Sprint(port))}
	}
}

func doctorLocalIP(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, address := range addresses {
		candidate, _, err := net.ParseCIDR(address.String())
		if err == nil && ip.Equal(candidate) {
			return true
		}
	}
	return false
}

func readDoctorFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > 16384 {
		return nil, fmt.Errorf("Некорректный размер файла диагностики")
	}
	content := make([]byte, info.Size())
	_, err = file.ReadAt(content, 0)
	return content, err
}

func readDoctorCertificate(path string) (*x509.Certificate, error) {
	content, err := readDoctorFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(content)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("Нет сертификата")
	}
	return x509.ParseCertificate(block.Bytes)
}

func appendWANCheck(report *DoctorReport, input string) {
	if input == "" {
		report.add("wan_unknown", "unknown", "")
		return
	}
	ip := net.ParseIP(strings.TrimSpace(input))
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
		report.add("wan_invalid", "failed", "")
		return
	}
	_, shared, _ := net.ParseCIDR("100.64.0.0/10")
	switch {
	case shared.Contains(ip):
		report.add("wan_shared", "warning", ip.String())
	case ip.To4() == nil && ip.IsPrivate():
		report.add("wan_ipv6_private", "warning", ip.String())
	case ip.IsPrivate():
		report.add("wan_private", "warning", ip.String())
	default:
		report.add("wan_public", "ok", ip.String())
	}
}
