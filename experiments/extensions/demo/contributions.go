package demo

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sort"

	"github.com/space-protocol-project/space-server/experiments/extensions/manifest"
)

// Contributions читает неизменяемую регистрацию без запуска WASM.
// Имена и сам факт наличия элементов не выдаются без актуального read-допуска.
func (d *Dispatcher) Contributions(ctx context.Context, credential, instance string) ([]manifest.Contribution, [32]byte, error) {
	var digest [32]byte
	r, ok := d.registrations[instance]
	if !ok {
		return nil, digest, ErrDenied
	}
	before, err := d.authorize(ctx, credential, instance, false)
	if err != nil {
		return nil, digest, authorizationError(ctx, err)
	}
	if !validPrincipal(before, instance) {
		return nil, digest, ErrDenied
	}
	items, err := registrationContributions(r)
	if err != nil {
		return nil, digest, err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, digest, ErrRequest
	}
	after, err := d.authorize(ctx, credential, instance, false)
	if err != nil {
		return nil, digest, authorizationError(ctx, err)
	}
	if after != before {
		return nil, digest, ErrDenied
	}
	if ctx.Err() != nil {
		return nil, digest, ctx.Err()
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, sha256.Sum256(raw), nil
}

func registrationContributions(r Registration) ([]manifest.Contribution, error) {
	query := r.Manifest.Query
	if len(r.SourceManifest) != 0 {
		m, err := supportedManifest(r.SourceManifest)
		if err != nil {
			return nil, err
		}
		if m.Contributions != nil {
			return m.Contributions, nil
		}
		if m.Queries["overview"] == "" {
			return nil, nil
		}
		query = "overview"
	}
	return []manifest.Contribution{{ID: "overview", Kind: "overview", Query: query, Title: map[string]string{"en": "Overview", "ru": "Обзор"}}}, nil
}
