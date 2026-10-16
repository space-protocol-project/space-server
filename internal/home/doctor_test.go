package home

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type doctorFixture struct {
	sync.Mutex
	document map[string]any
	redirect bool
	status   int
	requests int
}

func (fixture *doctorFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fixture.Lock()
	defer fixture.Unlock()
	fixture.requests++
	if fixture.status != 0 {
		w.WriteHeader(fixture.status)
		return
	}
	if fixture.redirect {
		http.Redirect(w, r, "https://127.0.0.1:1/", http.StatusFound)
		return
	}
	_ = json.NewEncoder(w).Encode(fixture.document)
}

func newDoctorFixture(t *testing.T) (*httptest.Server, *doctorFixture, Endpoint, string, string) {
	t.Helper()
	fixture := &doctorFixture{}
	server := httptest.NewTLSServer(fixture)
	t.Cleanup(server.Close)
	endpoint, err := EndpointFromOrigin(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(server.Certificate().Raw)
	pin := hex.EncodeToString(digest[:])
	serverID := "srv_" + strings.Repeat("1", 32)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := EndpointPayload(serverID, endpoint.Origin(), 1, pin, []Endpoint{endpoint})
	fixture.Lock()
	fixture.document = map[string]any{
		"server_id": serverID, "signing_public_key": base64.RawURLEncoding.EncodeToString(public),
		"protocol_version": "0.1-experimental", "signing_algorithm": "Ed25519",
		"identity_origin": endpoint.Origin(), "tls_fingerprint": pin,
		"endpoints": []Endpoint{endpoint}, "endpoint_revision": 1,
		"endpoint_signature": base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, payload)),
	}
	fixture.Unlock()
	return server, fixture, endpoint, pin, serverID
}

func TestDoctorPinnedTLSAndSignedIdentity(t *testing.T) {
	_, fixture, endpoint, pin, id := newDoctorFixture(t)
	report, err := Diagnose(context.Background(), DoctorOptions{Endpoints: []Endpoint{endpoint}, Fingerprint: pin, ServerID: id})
	if err != nil {
		t.Fatal(err)
	}
	if report.External != "unverified" || report.Checks[1].Code != "route_ok" {
		t.Fatalf("%+v", report)
	}
	if code := probeDoctorEndpoint(context.Background(), endpoint, "", strings.Repeat("0", 64), id); code != "certificate_mismatch" {
		t.Fatal(code)
	}
	if code := probeDoctorEndpoint(context.Background(), endpoint, "", pin, "srv_"+strings.Repeat("2", 32)); code != "discovery_invalid" {
		t.Fatal(code)
	}
	fixture.Lock()
	fixture.document["endpoint_revision"] = 2
	fixture.Unlock()
	if code := probeDoctorEndpoint(context.Background(), endpoint, "", pin, id); code != "discovery_invalid" {
		t.Fatal(code)
	}
}

func TestDoctorRefusesRedirectAndUnpinnedRemote(t *testing.T) {
	_, fixture, endpoint, pin, id := newDoctorFixture(t)
	fixture.Lock()
	fixture.redirect = true
	fixture.Unlock()
	if code := probeDoctorEndpoint(context.Background(), endpoint, "", pin, id); code != "discovery_invalid" {
		t.Fatal(code)
	}
	fixture.Lock()
	requests := fixture.requests
	fixture.Unlock()
	if requests != 1 {
		t.Fatal(requests)
	}
	if _, err := Diagnose(context.Background(), DoctorOptions{Endpoints: []Endpoint{endpoint}}); err == nil {
		t.Fatal("Проверка без якорей доверия")
	}
}

func TestDoctorWANDoesNotClaimInternetReachability(t *testing.T) {
	for input, code := range map[string]string{
		"": "wan_unknown", "100.64.0.1": "wan_shared", "100.127.255.254": "wan_shared",
		"192.168.1.2": "wan_private", "10.2.3.4": "wan_private", "fd00::1": "wan_ipv6_private",
		"203.0.113.1": "wan_public", "127.0.0.1": "wan_invalid", "garbage": "wan_invalid",
	} {
		report := DoctorReport{External: "unverified"}
		appendWANCheck(&report, input)
		if report.Checks[0].Code != code || report.External != "unverified" {
			t.Fatalf("%s: %+v", input, report)
		}
	}
}

func TestDoctorProbeBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	report, err := Diagnose(ctx, DoctorOptions{
		Endpoints:   []Endpoint{{Host: "example.org", Port: 443, Type: "https"}},
		Fingerprint: strings.Repeat("0", 64), ServerID: "srv_" + strings.Repeat("1", 32),
	})
	if err != nil || time.Since(start) > time.Second || report.Checks[1].Code != "budget_exhausted" {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestDoctorRejectsOversizedDiscovery(t *testing.T) {
	_, fixture, endpoint, pin, id := newDoctorFixture(t)
	fixture.Lock()
	fixture.document["padding"] = strings.Repeat("x", 16384)
	fixture.Unlock()
	if code := probeDoctorEndpoint(context.Background(), endpoint, "", pin, id); code != "discovery_invalid" {
		t.Fatal(code)
	}
}

func TestDoctorReportsUnfinishedSetupSeparately(t *testing.T) {
	_, fixture, endpoint, pin, id := newDoctorFixture(t)
	fixture.Lock()
	fixture.status = http.StatusServiceUnavailable
	fixture.Unlock()
	report, err := Diagnose(context.Background(), DoctorOptions{Endpoints: []Endpoint{endpoint}, Fingerprint: pin, ServerID: id})
	if err != nil || report.Checks[1].Code != "not_ready" || report.Checks[1].Status != "warning" {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestDoctorLocalAddressFamilies(t *testing.T) {
	addresses := localDoctorAddresses("::", 18443)
	if strings.Join(addresses, ",") != "[::1]:18443,127.0.0.1:18443" {
		t.Fatal(addresses)
	}
}
