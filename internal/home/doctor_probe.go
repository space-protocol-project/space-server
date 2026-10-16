package home

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"time"
)

var doctorServerID = regexp.MustCompile(`^srv_[0-9a-f]{32}$`)
var errDoctorPin = errors.New("TLS fingerprint mismatch")
var errDoctorTime = errors.New("certificate validity")

func validDoctorPin(pin string) bool {
	bytes, err := hex.DecodeString(pin)
	return err == nil && len(bytes) == 32 && pin == hex.EncodeToString(bytes)
}

func doctorTLS(pin string) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12,
		// Space использует закреплённый сертификат, а не PKI-имя адреса.
		InsecureSkipVerify: true,
		VerifyConnection: func(connection tls.ConnectionState) error {
			if len(connection.PeerCertificates) == 0 {
				return errDoctorPin
			}
			certificate := connection.PeerCertificates[0]
			digest := sha256.Sum256(certificate.Raw)
			if hex.EncodeToString(digest[:]) != pin {
				return errDoctorPin
			}
			now := time.Now()
			if now.Before(certificate.NotBefore) || now.After(certificate.NotAfter) {
				return errDoctorTime
			}
			return nil
		},
	}
}

func probeDoctorEndpoint(parent context.Context, endpoint Endpoint, dialAddress, pin, serverID string) string {
	if !validDoctorPin(pin) {
		return "certificate_missing"
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, TLSClientConfig: doctorTLS(pin), DisableKeepAlives: true,
		ResponseHeaderTimeout: 2 * time.Second, MaxResponseHeaderBytes: 16384}
	if dialAddress != "" {
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", dialAddress)
		}
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.Origin()+"/.well-known/space-protocol", nil)
	if err != nil {
		return "route_unreachable"
	}
	response, err := client.Do(request)
	if err != nil {
		return doctorNetworkError(err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusServiceUnavailable {
		return "not_ready"
	}
	if response.StatusCode != http.StatusOK {
		return "discovery_invalid"
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, 16385))
	if err != nil || len(content) > 16384 {
		return "discovery_invalid"
	}
	if err := verifyDoctorDiscovery(content, endpoint, pin, serverID); err != nil {
		return "discovery_invalid"
	}
	return "route_ok"
}

func doctorNetworkError(err error) string {
	var dns *net.DNSError
	switch {
	case errors.Is(err, errDoctorPin):
		return "certificate_mismatch"
	case errors.Is(err, errDoctorTime):
		return "certificate_expired"
	case errors.As(err, &dns):
		return "dns_failed"
	default:
		return "route_unreachable"
	}
}

func verifyDoctorDiscovery(content []byte, endpoint Endpoint, pin, serverID string) error {
	var document struct {
		ServerID    string     `json:"server_id"`
		PublicKey   string     `json:"signing_public_key"`
		Protocol    string     `json:"protocol_version"`
		Algorithm   string     `json:"signing_algorithm"`
		Identity    string     `json:"identity_origin"`
		Fingerprint string     `json:"tls_fingerprint"`
		Endpoints   []Endpoint `json:"endpoints"`
		Revision    int64      `json:"endpoint_revision"`
		Signature   string     `json:"endpoint_signature"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		return err
	}
	if !doctorServerID.MatchString(document.ServerID) || (serverID != "" && document.ServerID != serverID) || document.Fingerprint != pin || document.Protocol != "0.1-experimental" || document.Algorithm != "Ed25519" {
		return fmt.Errorf("Другая идентичность пространства")
	}
	if err := ValidateEndpoints(document.Endpoints); err != nil {
		return err
	}
	if _, err := EndpointFromOrigin(document.Identity); err != nil {
		return err
	}
	if document.Revision < 1 || document.Revision > MaxEndpointRevision {
		return fmt.Errorf("Некорректная версия адресов")
	}
	found := false
	for _, candidate := range document.Endpoints {
		if candidate.Origin() == endpoint.Origin() {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("Адрес не объявлен")
	}
	payload, err := EndpointPayload(document.ServerID, document.Identity, document.Revision, pin, document.Endpoints)
	if err != nil {
		return err
	}
	return checkDoctorSignature(document.PublicKey, document.Signature, payload)
}

func checkDoctorSignature(publicKey, signed string, payload []byte) error {
	key, keyErr := base64.RawURLEncoding.DecodeString(publicKey)
	signature, signatureErr := base64.RawURLEncoding.DecodeString(signed)
	if keyErr != nil || signatureErr != nil || len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("Некорректная подпись")
	}
	if !ed25519.Verify(key, payload, signature) {
		return fmt.Errorf("Недействительная подпись")
	}
	return nil
}
