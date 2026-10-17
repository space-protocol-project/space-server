package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"

	"github.com/space-protocol-project/space-server/internal/home"
	"github.com/space-protocol-project/space-server/internal/postgres"
	"github.com/space-protocol-project/space-server/internal/transport"
	"google.golang.org/grpc"
)

func homeIdentityOrigin(options serverOptions, publicOrigin string) (string, error) {
	if options.homeData == "" {
		return publicOrigin, nil
	}
	return home.IdentityOrigin(options.homeData, publicOrigin)
}

func homeCertificate(data, endpoint string) (tls.Certificate, error) {
	origin, err := home.IdentityOrigin(data, endpoint)
	if err != nil {
		return tls.Certificate{}, err
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return tls.Certificate{}, err
	}
	return home.Certificate(data, net.ParseIP(parsed.Hostname()))
}

func fixedHomeOrigin(ctx context.Context, handler http.Handler, store bindingSigner, identity postgres.Identity, identityOrigin, endpoint string, certificate tls.Certificate, endpoints []home.Endpoint, revision int64) (http.Handler, error) {
	digest := sha256.Sum256(certificate.Certificate[0])
	fingerprint := hex.EncodeToString(digest[:])
	signature, err := store.SignOriginBinding(ctx, identity.ServerID, identityOrigin, endpoint, fingerprint)
	if err != nil {
		return nil, err
	}
	payload, err := home.EndpointPayload(identity.ServerID, identityOrigin, revision, fingerprint, endpoints)
	if err != nil {
		return nil, err
	}
	signed, err := store.SignEndpoints(ctx, identity.ServerID, payload)
	if err != nil {
		return nil, err
	}
	return transport.WithOriginBinding(handler, identity.ServerID, identity.PublicKey, identityOrigin, endpoint, fingerprint, signature, map[string]any{
		"endpoints": endpoints, "endpoint_revision": revision, "endpoint_signature": signed,
	}), nil
}

func localHTTPHandler(ctx context.Context, connection *grpc.ClientConn, store *postgres.Store, identity postgres.Identity, config homeHTTPConfig, rpcEndpoint string) (http.Handler, error) {
	handler, err := transport.HandlerWithEndpoint(ctx, connection, identity.ServerID, identity.PublicKey, rpcEndpoint, true)
	if err != nil {
		return nil, err
	}
	if config.publicOrigin != "" {
		handler, err = bindHomeOrigin(ctx, handler, store, identity, config.identityOrigin, config.publicOrigin, config.certificate, config.data)
		if err != nil {
			return nil, err
		}
	}
	handler = store.WithPush(transport.WithConnect(handler, connection, storeVerifier(store)))
	return transport.RestrictOriginWithConnect(handler, config.localOrigin), nil
}

func homeEndpointsForBinding(data, fallback string) ([]home.Endpoint, int64, error) {
	config, err := home.ReadNetworkConfig(data)
	if err != nil {
		return nil, 0, err
	}
	if len(config.Endpoints) > 0 {
		return config.AdvertisedEndpoints(), config.Revision, nil
	}
	endpoint, err := home.EndpointFromOrigin(fallback)
	return []home.Endpoint{endpoint}, 1, err
}
