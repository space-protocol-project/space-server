package home

import (
	"bytes"
	"context"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/space-protocol-project/space-protocol/server/internal/postgres"
)

func TestCertificateSurvivesRestartAndRejectsChangedIP(t *testing.T) {
	directory := t.TempDir()
	ip := net.ParseIP("203.0.113.10")
	first, err := Certificate(directory, ip)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Certificate(directory, ip)
	if err != nil || !bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatal("Сертификат изменился", err)
	}
	leaf, err := x509.ParseCertificate(first.Certificate[0])
	if err != nil || leaf.VerifyHostname(ip.String()) != nil {
		t.Fatal("Нет IP в SAN", err)
	}
	if _, err := Certificate(directory, net.ParseIP("203.0.113.11")); err == nil {
		t.Fatal("Незаметная смена IP")
	}
	if _, err := os.Stat(filepath.Join(directory, "server.key")); err != nil {
		t.Fatal(err)
	}
}

func TestBundledPostgresPreservesIdentityAndData(t *testing.T) {
	root := os.Getenv("SPACE_HOME_RUNTIME")
	if root == "" {
		t.Skip("Проверяется на трёх runner с собранной PostgreSQL")
	}
	data := t.TempDir()
	var original string
	for attempt := 0; attempt < 2; attempt++ {
		address, stop, err := StartDatabase(root, data)
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer stop()
			store, identity, err := postgres.Open(context.Background(), address)
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
			if attempt == 0 {
				original = identity.ServerID
			} else if original != identity.ServerID {
				t.Fatal("server ID изменился")
			}
			connection, err := pgx.Connect(context.Background(), address)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close(context.Background())
			if attempt == 0 {
				_, err = connection.Exec(context.Background(), "CREATE TABLE home_probe(value text); INSERT INTO home_probe VALUES('saved')")
			} else {
				var value string
				err = connection.QueryRow(context.Background(), "SELECT value FROM home_probe").Scan(&value)
				if value != "saved" {
					t.Fatal("Данные потеряны")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
		}()
	}
}
