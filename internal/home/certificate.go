package home

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Certificate сохраняет сертификат между запусками: ссылки друзей не теряют отпечаток.
func Certificate(data string, ip net.IP) (tls.Certificate, error) {
	certPath, keyPath := filepath.Join(data, "server.crt"), filepath.Join(data, "server.key")
	if _, err := os.Stat(certPath); err == nil {
		certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return tls.Certificate{}, err
		}
		leaf, err := x509.ParseCertificate(certificate.Certificate[0])
		if err != nil {
			return tls.Certificate{}, err
		}
		if err := leaf.VerifyHostname(ip.String()); err != nil {
			return tls.Certificate{}, fmt.Errorf("IP изменился; нужна явная смена сертификата и ссылок: %w", err)
		}
		if time.Now().After(leaf.NotAfter) {
			return tls.Certificate{}, fmt.Errorf("Сертификат истёк; нужна явная смена сертификата")
		}
		return certificate, nil
	} else if !os.IsNotExist(err) {
		return tls.Certificate{}, err
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		return tls.Certificate{}, fmt.Errorf("Есть ключ без сертификата; автоматическая замена запрещена")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Space home server"}, NotBefore: time.Now().Add(-5 * time.Minute), NotAfter: time.Now().AddDate(5, 0, 0), IPAddresses: []net.IP{ip}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}
