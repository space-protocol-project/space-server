package home

import (
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testWitnessPolicy() witnessPolicy {
	e := Endpoint{Host: "8.8.8.8", Port: 8443, Type: "https"}
	return witnessPolicy{valid: true, targets: []witnessTarget{{endpoint: e, addresses: []netip.Addr{netip.MustParseAddr(e.Host)}}}, own: []netip.Addr{netip.MustParseAddr(e.Host), netip.MustParseAddr("2606:4700:1::1")}}
}

func TestWitnessRejectsLocalOwnReservedAndSameNetwork(t *testing.T) {
	policy := testWitnessPolicy()
	for _, remote := range []string{"127.0.0.1:10", "192.168.1.2:10", "100.64.1.2:10", "8.8.8.8:10", "8.8.8.9:10", "[2606:4700:1::2]:10", "[::1]:10", "203.0.113.1:10", "[2001:db8::1]:10", "[::ffff:192.168.1.2]:10", "[2002:808:808::1]:10"} {
		if got := policy.externalOrigin("8.8.8.8:8443", remote, ""); got != "" {
			t.Fatalf("Принят %s: %s", remote, got)
		}
	}
	if policy.externalOrigin("8.8.8.8:8443", "1.1.1.1:123", "") == "" {
		t.Fatal("Отклонена внешняя сеть")
	}
	if policy.externalOrigin("8.8.8.8:8443", "[2606:4700:2::1]:123", "") == "" {
		t.Fatal("Отклонена другая IPv6 сеть")
	}
	for _, authority := range []string{"other.example:8443", "8.8.8.8:9443", "8.8.8.8:8443/path"} {
		if policy.externalOrigin(authority, "1.1.1.1:123", "") != "" {
			t.Fatal(authority)
		}
	}
	if policy.externalOrigin("8.8.8.8:8443", "1.1.1.1:123", "other.example") != "" {
		t.Fatal("Несовпадение SNI")
	}
	policy.valid = false
	if policy.externalOrigin("8.8.8.8:8443", "1.1.1.1:123", "") != "" {
		t.Fatal("Неизвестный DNS принят за подтверждение")
	}
}

func newTestWitness(t *testing.T) (*NetworkWitness, DoctorReport) {
	t.Helper()
	data := t.TempDir()
	// На Windows удаление переименованного файла может завершиться с задержкой.
	// Очищаем только собственный TempDir до штатной очистки testing.
	t.Cleanup(func() {
		var err error
		for attempt := 0; attempt < 20; attempt++ {
			if err = os.RemoveAll(data); err == nil {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Errorf("Не удалось очистить каталог наблюдателя: %v", err)
	})
	endpoint := testWitnessPolicy().targets[0].endpoint
	w, err := NewNetworkWitness(context.Background(), data, "srv_"+strings.Repeat("1", 32), strings.Repeat("a", 64), []Endpoint{endpoint})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)
	w.mu.Lock()
	w.policy = testWitnessPolicy()
	w.mu.Unlock()
	report := DoctorReport{External: "unverified", ServerID: w.document.ServerID, Fingerprint: w.document.Fingerprint, Endpoints: []Endpoint{endpoint}}
	return w, report
}

func TestWitnessFreshnessBindingAndPrivacy(t *testing.T) {
	w, report := newTestWitness(t)
	now := time.Now()
	w.Observe("8.8.8.8:8443", "1.1.1.1:43210", "", now)
	content, err := os.ReadFile(w.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "1.1.1.1") || strings.Contains(string(content), "43210") {
		t.Fatal("Сохранён адрес наблюдателя")
	}
	appendExternalWitness(&report, filepath.Dir(w.path), now.Add(time.Second))
	if report.External != "observed" || report.ExternalCheckedAt != now.UnixMilli() {
		t.Fatalf("%+v", report)
	}
	for _, at := range []time.Time{now.Add(-time.Second), now.Add(91 * time.Second)} {
		copy := report
		copy.External = "unverified"
		copy.ExternalCheckedAt = 0
		copy.Checks = nil
		appendExternalWitness(&copy, filepath.Dir(w.path), at)
		if copy.External != "unverified" {
			t.Fatalf("Устаревшее/будущее подтверждение: %+v", copy)
		}
	}
	report.Fingerprint = strings.Repeat("b", 64)
	report.External = "unverified"
	report.ExternalCheckedAt = 0
	appendExternalWitness(&report, filepath.Dir(w.path), now.Add(time.Second))
	if report.External != "unverified" {
		t.Fatal("Другой сертификат принят")
	}
}

func TestWitnessResetStopRateAndChangedEndpoints(t *testing.T) {
	w, report := newTestWitness(t)
	now := time.Now()
	w.Observe("8.8.8.8:8443", "1.1.1.1:1000", "", now)
	w.Observe("8.8.8.8:8443", "1.1.1.1:1000", "", now.Add(time.Second))
	content, _ := os.ReadFile(w.path)
	var document witnessDocument
	if err := json.Unmarshal(content, &document); err != nil || len(document.Observations) != 1 || document.Observations[0].At != now.UnixMilli() {
		t.Fatal("Не действует ограничение записи")
	}
	report.Endpoints[0].Port = 9443
	appendExternalWitness(&report, filepath.Dir(w.path), now.Add(time.Second))
	if report.External != "unverified" {
		t.Fatal("Другой список адресов принят")
	}
	w.Close()
	if _, err := os.Stat(w.path); !os.IsNotExist(err) {
		t.Fatal("Остановка не сбросила подтверждение")
	}
	w.Observe("8.8.8.8:8443", "1.1.1.1:1000", "", now.Add(20*time.Second))
	if _, err := os.Stat(w.path); !os.IsNotExist(err) {
		t.Fatal("Запись после остановки")
	}
}

func TestWitnessLimitsFailedDiskWrites(t *testing.T) {
	w, _ := newTestWitness(t)
	directory := filepath.Join(filepath.Dir(w.path), "missing")
	w.path = filepath.Join(directory, "network-witness.json")
	now := time.Now()
	w.Observe("8.8.8.8:8443", "1.1.1.1:123", "", now)
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	w.Observe("8.8.8.8:8443", "1.1.1.1:123", "", now.Add(time.Second))
	if _, err := os.Stat(w.path); !os.IsNotExist(err) {
		t.Fatal("Сбой диска привёл к частым повторным записям")
	}
	w.Observe("8.8.8.8:8443", "1.1.1.1:123", "", now.Add(11*time.Second))
	if _, err := os.Stat(w.path); err != nil {
		t.Fatal("Запись не восстановилась после интервала", err)
	}
}
