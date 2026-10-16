package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/space-protocol-project/space-server/internal/home"
)

func observeHomeNetwork(ctx context.Context, handler http.Handler, config homeHTTPConfig, serverID string) (http.Handler, *home.NetworkWitness, error) {
	endpoints, _, err := homeEndpointsForBinding(config.data, config.publicOrigin)
	if err != nil {
		return nil, nil, err
	}
	digest := sha256.Sum256(config.certificate.Certificate[0])
	witness, err := home.NewNetworkWitness(ctx, config.data, serverID, hex.EncodeToString(digest[:]), endpoints)
	if err != nil {
		return nil, nil, err
	}
	return witness.Handler(handler), witness, nil
}
