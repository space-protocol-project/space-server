package home

import (
	"bytes"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInvalidSavedCertificateIsNotReplaced(t *testing.T) {
	for _, scenario := range []string{"expired", "missing-key", "corrupt-certificate"} {
		t.Run(scenario, func(t *testing.T) {
			data := t.TempDir()
			ip := net.ParseIP("203.0.113.10")
			certificate, err := Certificate(data, ip)
			if err != nil {
				t.Fatal(err)
			}
			certPath := filepath.Join(data, "server.crt")
			keyPath := filepath.Join(data, "server.key")
			switch scenario {
			case "expired":
				leaf, err := x509.ParseCertificate(certificate.Certificate[0])
				if err != nil {
					t.Fatal(err)
				}
				leaf.NotBefore = time.Now().Add(-48 * time.Hour)
				leaf.NotAfter = time.Now().Add(-24 * time.Hour)
				der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, leaf.PublicKey, certificate.PrivateKey)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-key":
				if err := os.Remove(keyPath); err != nil {
					t.Fatal(err)
				}
			case "corrupt-certificate":
				if err := os.WriteFile(certPath, []byte("invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(certPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Certificate(data, ip); err == nil {
				t.Fatal("Ошибочный сертификат принят")
			}
			after, err := os.ReadFile(certPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("Сертификат незаметно заменён", err)
			}
			if scenario == "missing-key" {
				if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
					t.Fatal("Удалённый ключ восстановлен", err)
				}
			}
		})
	}
}
