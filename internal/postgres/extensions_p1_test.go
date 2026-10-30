package postgres

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"sync/atomic"
	"testing"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/demo"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"github.com/space-protocol-project/space-server/internal/extensionp1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
)

type extensionP1Probe struct {
	calls  atomic.Int32
	during func([]byte)
}

func (h *extensionP1Probe) Invoke(_ context.Context, call extensions.Invocation) ([]byte, error) {
	h.calls.Add(1)
	if h.during != nil {
		h.during(call.Input)
	}
	return []byte(`{"ui_api":"space.ui.p1"}`), nil
}
func (*extensionP1Probe) Status() extensions.RuntimeStatus {
	return extensions.RuntimeStatus{State: "ready"}
}
func (*extensionP1Probe) Close() error { return nil }

func extensionP1Client(t *testing.T, f *channelFixture, host extensions.Runtime, module []byte, channel string) pb.ExtensionP1ServiceClient {
	t.Helper()
	manifest := demo.Manifest{HostAPI: "space.plugin.p1", ABI: "space_plugin_p1", UI: "space.ui.p1", Contribution: "overview", Query: "overview", Command: "acknowledge"}
	svc, err := extensionp1.New(host, f.store.ExtensionP1Authorizer(map[string]string{"demo": channel}), map[string]demo.Registration{"demo": {Manifest: manifest, Module: module}})
	if err != nil {
		t.Fatal(err)
	}
	return extensionP1RPCClient(t, f, svc)
}

func extensionP1RPCClient(t *testing.T, f *channelFixture, svc pb.ExtensionP1ServiceServer) pb.ExtensionP1ServiceClient {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.MaxRecvMsgSize(20*1024), grpc.UnaryInterceptor(authn.Interceptor(f.store)))
	pb.RegisterExtensionP1ServiceServer(server, svc)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return pb.NewExtensionP1ServiceClient(conn)
}

func extensionP1Query() *pb.QueryExtensionRequest {
	return &pb.QueryExtensionRequest{InstanceId: "demo", Query: "overview", PayloadJson: []byte(`{}`)}
}
func extensionP1Command() *pb.ExecuteExtensionCommandRequest {
	return &pb.ExecuteExtensionCommandRequest{InstanceId: "demo", Command: "acknowledge", PayloadJson: []byte(`{}`), Effect: pb.ExtensionCommandEffect_EXTENSION_COMMAND_EFFECT_NONE}
}

func TestExtensionP1SessionAndACL(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "extension_acl")
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	member, id := f.enroll(t, true)
	host := new(extensionP1Probe)
	api := extensionP1Client(t, f, host, []byte{1}, c.Id)
	_, err := api.QueryExtension(f.ctx, extensionP1Query())
	requireCode(t, err, codes.Unauthenticated)
	_, err = api.QueryExtension(member, extensionP1Query())
	if err != nil || host.calls.Load() != 1 {
		t.Fatal(err, host.calls.Load())
	}
	f.acl(t, c, roleRule("member", permissions(false, false, false, false)))
	_, err = api.QueryExtension(member, extensionP1Query())
	requireCode(t, err, codes.PermissionDenied)
	if host.calls.Load() != 1 {
		t.Fatal("WASM вызван без доступа")
	}
	f.acl(t, c, roleRule("reader", permissions(true, true, true, false)))
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE memberships SET role='reader',revision=revision+1 WHERE principal_id=$1", id); err != nil {
		t.Fatal(err)
	}
	_, err = api.QueryExtension(member, extensionP1Query())
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.ExecuteExtensionCommand(member, extensionP1Command())
	requireCode(t, err, codes.PermissionDenied)
	if host.calls.Load() != 2 {
		t.Fatal("Reader выполнил команду")
	}
}

func TestExtensionP1InvalidSessionsNeverInvoke(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "extension_sessions")
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	for _, test := range []struct {
		name, sql string
		code      codes.Code
	}{
		{"expired", "UPDATE auth_sessions SET expires_at=now()-interval '1 second' WHERE grant_id IN (SELECT id FROM device_grants WHERE principal_id=$1)", codes.Unauthenticated},
		{"revoked", "UPDATE device_grants SET revoked_at=now() WHERE principal_id=$1", codes.Unauthenticated},
		{"epoch", "UPDATE principals SET auth_epoch=auth_epoch+1 WHERE id=$1", codes.Unauthenticated},
		{"push_only", "UPDATE auth_sessions SET push_only=true WHERE grant_id IN (SELECT id FROM device_grants WHERE principal_id=$1)", codes.Unauthenticated},
		{"scopes", "UPDATE device_grants SET scopes=ARRAY['identity.recover'] WHERE principal_id=$1", codes.PermissionDenied},
		{"blocked", "UPDATE memberships SET blocked=true,revision=revision+1 WHERE principal_id=$1", codes.PermissionDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			member, id := f.enroll(t, true)
			host := new(extensionP1Probe)
			api := extensionP1Client(t, f, host, []byte{1}, c.Id)
			if _, err := f.store.pool.Exec(f.ctx, test.sql, id); err != nil {
				t.Fatal(err)
			}
			_, err := api.QueryExtension(member, extensionP1Query())
			requireCode(t, err, test.code)
			if host.calls.Load() != 0 {
				t.Fatal("Вызван WASM с недействительным доступом")
			}
		})
	}
}

func TestExtensionP1RecheckAfterWASM(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "extension_recheck")
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	for _, sql := range []string{
		"DELETE FROM auth_sessions WHERE grant_id IN (SELECT id FROM device_grants WHERE principal_id=$1)",
		"UPDATE memberships SET revision=revision+1 WHERE principal_id=$1",
		"UPDATE channels SET revision=revision+1 WHERE id='extension_recheck' AND $1<>''",
	} {
		member, id := f.enroll(t, true)
		host := &extensionP1Probe{during: func(input []byte) {

			// Проверяем именно доверенные JSON-поля, не клиентскую подстановку.
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(input, &raw); err != nil {
				t.Error(err)
			}
			var trusted map[string]string
			if err := json.Unmarshal(raw["context"], &trusted); err != nil {
				t.Error(err)
			}
			if trusted["principal_id"] != id || trusted["space_id"] != f.identity.ServerID || trusted["device_id"] == "" {
				t.Error("Недоверенный context")
			}
			if _, err := f.store.pool.Exec(f.ctx, sql, id); err != nil {
				t.Error(err)
			}
		}}
		api := extensionP1Client(t, f, host, []byte{1}, c.Id)
		_, err := api.QueryExtension(member, extensionP1Query())
		requireCode(t, err, codes.PermissionDenied)
		if host.calls.Load() != 1 {
			t.Fatal("Не проверен отзыв во время вызова")
		}
	}
}

func TestExtensionP1RejectsMutationAndPayload(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "extension_contract")
	host := new(extensionP1Probe)
	api := extensionP1Client(t, f, host, []byte{1}, c.Id)
	for _, payload := range []string{`{"context":{"principal_id":"owner"}}`, `{"a":1,"a":2}`, `null`} {
		r := extensionP1Query()
		r.PayloadJson = []byte(payload)
		_, err := api.QueryExtension(f.owner, r)
		requireCode(t, err, codes.InvalidArgument)
	}
	r := extensionP1Command()
	r.Effect = pb.ExtensionCommandEffect_EXTENSION_COMMAND_EFFECT_MUTATION
	r.IdempotencyKey = "mutation-test-001"
	r.ExpectedStateRevision = 1
	_, err := api.ExecuteExtensionCommand(f.owner, r)
	requireCode(t, err, codes.FailedPrecondition)
	if host.calls.Load() != 0 {
		t.Fatal("Недопустимый запрос достиг WASM")
	}
	_, err = api.ExecuteExtensionCommand(f.owner, extensionP1Command())
	if err != nil {
		t.Fatal(err)
	}
}

func TestExtensionP1RealIsolatedWASM(t *testing.T) {
	worker := os.Getenv("SPACE_P1_WORKER")
	if worker == "" {
		t.Skip("Отдельный CI с cgroup и WASM worker")
	}
	f := newChannelFixture(t)
	c := f.create(t, "extension_real")
	host, err := extensions.NewProcessHost(os.Getenv("SPACE_P1_CGROUP"), worker)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	module, err := os.ReadFile(os.Getenv("SPACE_P1_MODULE"))
	if err != nil {
		t.Fatal(err)
	}
	api := extensionP1Client(t, f, host, module, c.Id)
	q, err := api.QueryExtension(f.owner, extensionP1Query())
	if err != nil || q.GetUiApi() != "space.ui.p1" {
		t.Fatal(q, err)
	}
	r, err := api.ExecuteExtensionCommand(f.owner, extensionP1Command())
	if err != nil || string(q.DocumentJson) == string(r.GetDocumentJson()) {
		t.Fatal(r, err)
	}
}
