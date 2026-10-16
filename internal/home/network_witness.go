package home

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const witnessLifetime = 90 * time.Second

type NetworkObservation struct {
	Origin string `json:"origin"`
	At     int64  `json:"at_unix_ms"`
}

type witnessDocument struct {
	Format        int                  `json:"format"`
	ServerID      string               `json:"server_id"`
	Fingerprint   string               `json:"fingerprint"`
	EndpointsHash string               `json:"endpoints_hash"`
	Observations  []NetworkObservation `json:"observations"`
}

// Только краткоживущие сведения о входящем HTTPS; адреса участников и токены не сохраняются.
type NetworkWitness struct {
	mu        sync.Mutex
	path      string
	document  witnessDocument
	policy    witnessPolicy
	closed    bool
	lastWrite time.Time
	cancel    context.CancelFunc
}

func endpointWitnessHash(endpoints []Endpoint) string {
	content, _ := json.Marshal(endpoints)
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func NewNetworkWitness(ctx context.Context, data, id, pin string, endpoints []Endpoint) (*NetworkWitness, error) {
	if err := ValidateEndpoints(endpoints); err != nil {
		return nil, err
	}
	if !validDoctorPin(pin) || !doctorServerID.MatchString(id) {
		return nil, fmt.Errorf("Некорректная идентичность наблюдателя")
	}
	ctx, cancel := context.WithCancel(ctx)
	witness := &NetworkWitness{cancel: cancel, path: filepath.Join(data, "network-witness.json"), document: witnessDocument{
		Format: 1, ServerID: id, Fingerprint: pin, EndpointsHash: endpointWitnessHash(endpoints), Observations: []NetworkObservation{},
	}}
	// При каждом запуске прежнее свидетельство аннулируется до открытия слушателя.
	if err := witness.save(); err != nil {
		cancel()
		return nil, err
	}
	witness.refresh(ctx, endpoints)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				witness.Close()
				return
			case <-ticker.C:
				witness.refresh(ctx, endpoints)
			}
		}
	}()
	return witness, nil
}

func (w *NetworkWitness) refresh(ctx context.Context, endpoints []Endpoint) {
	policy := resolveWitnessPolicy(ctx, endpoints)
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.policy = policy
	}
}

// Вызывается только после успешного ответа публичного TLS discovery.
func (w *NetworkWitness) Observe(authority, remote, serverName string, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || now.Sub(w.lastWrite) < 10*time.Second {
		return
	}
	origin := w.policy.externalOrigin(authority, remote, serverName)
	if origin == "" {
		return
	}
	if !w.record(origin, now.UnixMilli()) {
		return
	}
	if w.save() == nil {
		w.lastWrite = now
	}
}

func (w *NetworkWitness) record(origin string, at int64) bool {
	for i, previous := range w.document.Observations {
		if previous.Origin == origin {
			w.document.Observations[i].At = at
			return true
		}
	}
	if len(w.document.Observations) >= 8 {
		return false
	}
	w.document.Observations = append(w.document.Observations, NetworkObservation{Origin: origin, At: at})
	return true
}

func (w *NetworkWitness) save() error {
	content, err := json.Marshal(w.document)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(w.path), ".network-witness-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), w.path)
}

func (w *NetworkWitness) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		if w.cancel != nil {
			w.cancel()
		}
		_ = os.Remove(w.path)
	}
}

func appendExternalWitness(report *DoctorReport, data string, now time.Time) {
	if data != "" {
		content, err := readDoctorFile(filepath.Join(data, "network-witness.json"))
		var document witnessDocument
		if err == nil && json.Unmarshal(content, &document) == nil && validWitnessDocument(document, *report) {
			appendWitnessObservations(report, document, now)
		}
	}
	if report.External == "unverified" {
		report.add("external_unverified", "unknown", "")
	}
}

func validWitnessDocument(document witnessDocument, report DoctorReport) bool {
	return validDoctorPin(report.Fingerprint) && doctorServerID.MatchString(report.ServerID) && document.Format == 1 && document.ServerID == report.ServerID && document.Fingerprint == report.Fingerprint &&
		document.EndpointsHash == endpointWitnessHash(report.Endpoints) && len(document.Observations) <= 8
}

func appendWitnessObservations(report *DoctorReport, document witnessDocument, now time.Time) {
	seen := map[string]bool{}
	for _, observation := range document.Observations {
		age := now.Sub(time.UnixMilli(observation.At))
		if seen[observation.Origin] || age < 0 || age > witnessLifetime || !witnessOriginAllowed(observation.Origin, report.Endpoints) {
			continue
		}
		seen[observation.Origin] = true
		report.add("external_observed", "ok", observation.Origin)
		report.External = "observed"
		if observation.At > report.ExternalCheckedAt {
			report.ExternalCheckedAt = observation.At
		}
	}
}

func witnessOriginAllowed(origin string, endpoints []Endpoint) bool {
	for _, endpoint := range endpoints {
		if endpoint.Origin() == origin {
			return true
		}
	}
	return false
}
