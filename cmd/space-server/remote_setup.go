package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
)

// До назначения владельца доступны только discovery и минимальные RPC настройки.
type setupStatusReader interface {
	GetSetupStatus(context.Context, *pb.GetSetupStatusRequest) (*pb.GetSetupStatusResponse, error)
	SealHomeSetup(context.Context) error
}

func remoteSetupHandler(ctx context.Context, next http.Handler, initialized *atomic.Bool, store setupStatusReader, config homeHTTPConfig) http.Handler {
	expires := time.Now().Add(15 * time.Minute)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if initialized.Load() {
			r.Header.Del("X-Space-Setup")
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/.well-known/space-protocol" {
			next.ServeHTTP(w, r)
			return
		}
		if !validSetupHeader(r, config.setupCode, expires) {
			http.Error(w, "Откройте одноразовую ссылку настройки из терминала сервера", http.StatusServiceUnavailable)
			return
		}
		// После ClaimOwner не ждём фонового опроса: новый владелец сразу работает.
		check, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		status, err := store.GetSetupStatus(check, &pb.GetSetupStatusRequest{})
		if err != nil {
			http.Error(w, "Не удалось проверить состояние настройки", http.StatusServiceUnavailable)
			return
		}
		if status.Initialized && finishHomeSetup(check, store, config.pendingSetupPath) == nil {
			initialized.Store(true)
		}
		if !initialized.Load() && !bootstrapProcedure(r) {
			http.Error(w, "Сначала завершите настройку владельца", http.StatusServiceUnavailable)
			return
		}
		r.Header.Del("X-Space-Setup")
		next.ServeHTTP(w, r)
	})
}

func validSetupHeader(r *http.Request, code string, expires time.Time) bool {
	values := r.Header.Values("X-Space-Setup")
	return len(values) == 1 && code != "" && time.Now().Before(expires) && subtle.ConstantTimeCompare([]byte(values[0]), []byte(code)) == 1
}

func bootstrapProcedure(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case rpc.AdminServiceGetSetupStatusProcedure, rpc.AdminServiceClaimOwnerProcedure,
		rpc.ChannelServiceGetManifestProcedure, rpc.AuthServiceCreateChallengeProcedure,
		rpc.AuthServiceCompleteChallengeProcedure:
		return true
	}
	return false
}

func printSetupLink(config homeHTTPConfig, serverID string) error {
	digest := sha256.Sum256(config.certificate.Certificate[0])
	fields := url.Values{"setup": {config.setupCode}, "tls": {hex.EncodeToString(digest[:])}, "server": {serverID}}
	_, err := fmt.Fprintf(config.output, "\nОткройте Space на компьютере или телефоне и вставьте ссылку:\n%s#%s\n\nПервый вошедший по ссылке станет владельцем. Ссылка действует 15 минут.\nНе публикуйте её. Для новой ссылки перезапустите сервер.\n", config.publicOrigin, fields.Encode())
	return err
}
