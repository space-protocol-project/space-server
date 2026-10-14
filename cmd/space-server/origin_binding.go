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

func bindHomeOrigin(ctx context.Context, handler http.Handler, store *postgres.Store, identity postgres.Identity, identityOrigin, endpoint string, certificate tls.Certificate) (http.Handler, error) {
	digest := sha256.Sum256(certificate.Certificate[0])
	fingerprint := hex.EncodeToString(digest[:])
	signature, err := store.SignOriginBinding(ctx, identity.ServerID, identityOrigin, endpoint, fingerprint)
	if err != nil {
		return nil, err
	}
	return transport.WithOriginBinding(handler, identity.ServerID, identity.PublicKey, identityOrigin, endpoint, fingerprint, signature), nil
}

func localHTTPHandler(ctx context.Context, connection *grpc.ClientConn, store *postgres.Store, identity postgres.Identity, config homeHTTPConfig, rpcEndpoint string) (http.Handler, error) {
	handler, err := transport.HandlerWithEndpoint(ctx, connection, identity.ServerID, identity.PublicKey, rpcEndpoint, true)
	if err != nil {
		return nil, err
	}
	if config.publicOrigin != "" {
		handler, err = bindHomeOrigin(ctx, handler, store, identity, config.identityOrigin, config.publicOrigin, config.certificate)
		if err != nil {
			return nil, err
		}
	}
	handler = transport.WithConnect(handler, connection, storeVerifier(store))
	return transport.RestrictOriginWithConnect(handler, config.localOrigin), nil
}
