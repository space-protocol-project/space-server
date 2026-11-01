package postgres

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestExtensionP1RegistryAPIRevocationBoundary(t *testing.T) {
	for _, method := range []string{"list", "get", "audit", "change"} {
		for _, kind := range []string{"session", "owner", "scope", "channel"} {
			t.Run(method+"/"+kind, func(t *testing.T) {
				f, s, api := registryAPIFixture(t)
				tx, err := f.store.pool.Begin(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(f.ctx)
				hash := sha256.Sum256([]byte(lifecycleToken(f.owner)))
				fragment := "SELECT id FROM device_grants"
				if kind == "owner" || kind == "channel" {
					if kind == "owner" {
						_, err = tx.Exec(f.ctx, "UPDATE space_settings SET owner_id=NULL,revision=revision+1 WHERE singleton=true")
					} else {
						_, err = tx.Exec(f.ctx, "UPDATE space_settings SET chat_enabled=false,revision=revision+1 WHERE singleton=true")
					}
					fragment = "SELECT singleton FROM space_settings"
				} else {
					_, err = tx.Exec(f.ctx, "SELECT id FROM device_grants WHERE id=(SELECT grant_id FROM auth_sessions WHERE token_hash=$1) FOR UPDATE", hash[:])
				}
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- registryAPICalls(api, s.Generation())[method](f.owner) }()
				waitExtensionSQLLock(t, f, fragment)
				switch kind {
				case "session":
					_, err = f.auth.Logout(f.owner, &pb.LogoutRequest{})
				case "scope":
					_, err = tx.Exec(f.ctx, "UPDATE device_grants SET scopes=ARRAY['chat.read','chat.write'] WHERE principal_id=$1", f.ownerID)

				}
				if err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(f.ctx); err != nil {
					t.Fatal(err)
				}
				err = <-done
				if kind == "channel" {
					requireCode(t, err, codes.NotFound)
				} else if status.Code(err) != codes.PermissionDenied && status.Code(err) != codes.Unauthenticated {
					t.Fatal(err)
				}
				lifecycleAuditCount(t, f, 0)
			})
		}
	}
}

func TestExtensionP1RegistryAPIPaginationAndFencing(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "pages")
	bindings := mutationBindings(c.Id, []byte{1})
	b := bindings["demo"]
	b.Installation = "second"
	bindings["second"] = b
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { t.Error("Guest called"); return nil, nil }}
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, h, bindings)
	if err != nil {
		t.Fatal(err)
	}
	api := extensionP1RPCClient(t, f, s)
	first, err := api.ListExtensionInstallations(f.owner, &pb.ListExtensionInstallationsRequest{PageSize: 1})
	if err != nil || len(first.GetInstallations()) != 1 || first.GetNextAfterInstallationId() != "install" {
		t.Fatal(first, err)
	}
	next, err := api.ListExtensionInstallations(f.owner, &pb.ListExtensionInstallationsRequest{PageSize: 1, Generation: first.Generation, AfterInstallationId: first.NextAfterInstallationId})
	if err != nil || len(next.GetInstallations()) != 1 || next.Installations[0].InstallationId != "second" || next.NextAfterInstallationId != "" {
		t.Fatal(next, err)
	}
	store, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	newer, err := store.NewExtensionP1ManagedService(f.ctx, h, bindings)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range registryAPICalls(api, s.Generation()) {
		requireCode(t, call(f.owner), codes.FailedPrecondition)
	}
	_, err = api.ListExtensionInstallations(f.owner, &pb.ListExtensionInstallationsRequest{AfterInstallationId: "zzzz", Generation: s.Generation()})
	requireCode(t, err, codes.FailedPrecondition)
	current := extensionP1RPCClient(t, f, newer)
	_, err = current.ListExtensionInstallationAudit(f.owner, &pb.ListExtensionInstallationAuditRequest{InstallationId: "install", Generation: s.Generation()})
	requireCode(t, err, codes.FailedPrecondition)
}

func TestExtensionP1RegistryAPIConnect(t *testing.T) {
	f, s, api := registryAPIFixture(t)
	path, handler := transport.NewExtensionP1Handler(api)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := rpc.NewExtensionP1ServiceClient(server.Client(), server.URL)
	request := connect.NewRequest(&pb.ListExtensionInstallationsRequest{})
	_, err := client.ListExtensionInstallations(f.ctx, request)
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatal(err)
	}
	request.Header().Set("Authorization", outgoingAuthorization(f.owner))
	result, err := client.ListExtensionInstallations(f.ctx, request)
	if err != nil || len(result.Msg.Installations) != 1 {
		t.Fatal(result, err)
	}
	change := connect.NewRequest(&pb.ChangeExtensionInstallationRequest{InstallationId: "install", Generation: s.Generation(), ExpectedRevision: 1, Action: "enable"})
	change.Header().Set("Authorization", outgoingAuthorization(f.owner))
	if _, err = client.ChangeExtensionInstallation(f.ctx, change); err != nil {
		t.Fatal(err)
	}
	audit := connect.NewRequest(&pb.ListExtensionInstallationAuditRequest{InstallationId: "install", Generation: s.Generation()})
	audit.Header().Set("Authorization", outgoingAuthorization(f.owner))
	history, err := client.ListExtensionInstallationAudit(f.ctx, audit)
	if err != nil || len(history.Msg.Entries) != 2 {
		t.Fatal(history, err)
	}
	get := connect.NewRequest(&pb.GetExtensionInstallationRequest{InstallationId: strings.Repeat("x", 21*1024)})
	get.Header().Set("Authorization", outgoingAuthorization(f.owner))
	_, err = client.GetExtensionInstallation(f.ctx, get)
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatal(err)
	}
}
