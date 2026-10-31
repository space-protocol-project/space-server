package home

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/demo"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"github.com/space-protocol-project/space-server/internal/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type backupExtensionHost struct{ calls atomic.Int32 }

func (h *backupExtensionHost) Invoke(context.Context, extensions.Invocation) ([]byte, error) {
	h.calls.Add(1)
	return []byte(`{"ui_api":"space.ui.p1","document":{"ui_api":"space.ui.p1"},"plan":{"expectedStateRevision":"0","operations":[{"key":"note","putJson":"eyJ2YWx1ZSI6MX0="}],"events":[{"type":"note_changed","payloadJson":"e30="},{"type":"note_changed","payloadJson":"e30="},{"type":"note_changed","payloadJson":"e30="}]}}`), nil
}
func (*backupExtensionHost) Status() extensions.RuntimeStatus {
	return extensions.RuntimeStatus{State: "ready"}
}
func (*backupExtensionHost) Close() error { return nil }

func backupExtensionBindings() map[string]postgres.ExtensionP1MutationBinding {
	return map[string]postgres.ExtensionP1MutationBinding{"demo": {Installation: "install", Channel: "general", Registration: demo.Registration{Module: []byte{1}, Manifest: demo.Manifest{HostAPI: "space.plugin.p1", ABI: "space_plugin_p1", UI: "space.ui.p1", Contribution: "overview", Query: "overview", Command: "acknowledge"}}, Events: []string{"note_changed"}}}
}

func backupExtensionProof(t *testing.T, auth *postgres.AuthService, request *pb.CreateChallengeRequest, key ed25519.PrivateKey) *pb.CompleteChallengeResponse {
	t.Helper()
	ctx := context.Background()
	challenge, err := auth.CreateChallenge(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var transcript authn.Transcript
	if err = json.Unmarshal(challenge.Transcript, &transcript); err != nil {
		t.Fatal(err)
	}
	raw, err := transcript.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	response, err := auth.CompleteChallenge(ctx, &pb.CompleteChallengeRequest{ChallengeId: challenge.ChallengeId, Signature: ed25519.Sign(key, raw)})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func backupExtensionLogin(t *testing.T, s *postgres.Store, id postgres.Identity, grant string, key ed25519.PrivateKey) (context.Context, string) {
	t.Helper()
	auth := postgres.NewAuth(s, id.ServerID, "http://127.0.0.1:8080")
	login := backupExtensionProof(t, auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: grant}, key)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+login.AccessToken))
	ctx, err := authn.AuthorizeCall(ctx, s, "/space.v1.ExtensionP1Service/ExecuteExtensionCommand")
	if err != nil {
		t.Fatal(err)
	}
	return ctx, login.AccessToken
}

func TestExtensionP1BackupRoundTrip(t *testing.T) {
	runtime := os.Getenv("SPACE_HOME_RUNTIME")
	if runtime == "" {
		t.Skip("Нужен настоящий домашний runtime")
	}
	for _, portable := range []bool{false, true} {
		name := "physical"
		if portable {
			name = "portable"
		}
		for _, retired := range []bool{false, true} {
			suffix := "_full"
			if retired {
				suffix = "_retired"
			}
			t.Run(name+suffix, func(t *testing.T) { extensionBackupRoundTrip(t, runtime, portable, retired) })
		}
	}
}

func extensionBackupRoundTrip(t *testing.T, runtime string, portable, retired bool) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	address, stop, err := StartDatabase(runtime, data)
	if err != nil {
		t.Fatal(err)
	}
	active := true
	defer func() {
		if active {
			if err := stop(); err != nil {
				t.Error(err)
			}
		}
	}()
	s, id, err := postgres.Open(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	rootKey, rootPrivate, _ := ed25519.GenerateKey(rand.Reader)
	device, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
	auth := postgres.NewAuth(s, id.ServerID, "http://127.0.0.1:8080")
	grant := backupExtensionProof(t, auth, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: rootKey, DevicePublicKey: device, Administrative: true}, rootPrivate)
	member, oldToken := backupExtensionLogin(t, s, id, grant.GrantId, devicePrivate)
	host := new(backupExtensionHost)
	svc, err := s.NewExtensionP1MutationService(ctx, host, backupExtensionBindings())
	if err != nil {
		t.Fatal(err)
	}
	request := &pb.ExecuteExtensionCommandRequest{InstanceId: "demo", Command: "acknowledge", PayloadJson: []byte(`{}`), Effect: pb.ExtensionCommandEffect_EXTENSION_COMMAND_EFFECT_MUTATION, IdempotencyKey: "backup-command-001"}
	first, err := svc.ExecuteExtensionCommand(member, request)
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := s.NewExtensionP1Outbox(ctx, "install", "demo", "projection")
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := outbox.Claim(ctx)
	if err != nil || confirmed == nil {
		t.Fatal(confirmed, err)
	}
	if err = outbox.Ack(ctx, confirmed.Token); err != nil {
		t.Fatal(err)
	}
	leased, err := outbox.Claim(ctx)
	if err != nil || leased == nil || leased.Ordinal != 1 {
		t.Fatal(leased, err)
	}
	if _, err = Certificate(data, net.ParseIP("203.0.113.10")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"ip":"203.0.113.10","port":8443}`), 0600); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if retired {
		retireBackupExtensionReceipt(t, address)
	}
	if err = stop(); err != nil {
		t.Fatal(err)
	}
	active = false
	backup, destination := filepath.Join(root, "backup"), filepath.Join(root, "restored")
	if portable {
		backup += ".zip"
		if err = ExportPortable(runtime, data, backup); err != nil {
			t.Fatal(err)
		}
		if err = ImportPortable(runtime, backup, destination, ""); err != nil {
			t.Fatal(err)
		}
	} else {
		if err = CreateBackup(runtime, data, backup); err != nil {
			t.Fatal(err)
		}
		if err = RestoreBackup(runtime, backup, destination); err != nil {
			t.Fatal(err)
		}
		if err = VerifyBackup(backup); err != nil {
			t.Fatal("Источник изменён", err)
		}
	}
	address, stop, err = StartDatabase(runtime, filepath.Join(destination, "data"))
	if err != nil {
		t.Fatal(err)
	}
	active = true
	var restored postgres.Identity
	s, restored, err = postgres.Open(ctx, address)
	if err != nil || restored.ServerID != id.ServerID {
		t.Fatal(restored, err)
	}
	if portable {
		if _, err = s.Authenticate(ctx, oldToken); status.Code(err) != codes.Unauthenticated {
			t.Fatal("Перенесена сессия", err)
		}
	}
	member, _ = backupExtensionLogin(t, s, restored, grant.GrantId, devicePrivate)
	host = new(backupExtensionHost)
	svc, err = s.NewExtensionP1MutationService(ctx, host, backupExtensionBindings())
	if err != nil {
		t.Fatal(err)
	}
	replay, err := svc.ExecuteExtensionCommand(member, request)
	if retired {
		if status.Code(err) != codes.FailedPrecondition || replay != nil || host.calls.Load() != 0 {
			t.Fatal(replay, err, host.calls.Load())
		}
	} else {
		if err != nil || !replay.GetReplayed() || host.calls.Load() != 0 {
			t.Fatal(replay, err, host.calls.Load())
		}
		replay.Replayed = false
		if !proto.Equal(first, replay) {
			t.Fatal("Квитанция изменилась")
		}
	}
	outbox, err = s.NewExtensionP1Outbox(ctx, "install", "demo", "projection")
	if err != nil {
		t.Fatal(err)
	}
	if err = outbox.Ack(ctx, leased.Token); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("Старая аренда принята", err)
	}
	if err = outbox.Ack(ctx, confirmed.Token); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("Старый ACK принят", err)
	}
	next, err := outbox.Claim(ctx)
	if err != nil || next == nil || next.ID != leased.ID || next.Ordinal != 1 || next.Token == leased.Token || next.Attempt != 2 {
		t.Fatal(next, err)
	}
	if err = outbox.Ack(ctx, next.Token); err != nil {
		t.Fatal(err)
	}
	pending, err := outbox.Claim(ctx)
	if err != nil || pending == nil || pending.Ordinal != 2 {
		t.Fatal(pending, err)
	}
	checkBackupExtensionData(t, address)
	t.Logf("portable=%v retired=%v guest_calls=0 cursor_preserved=true stale_tokens_rejected=true", portable, retired)
}

func checkBackupExtensionData(t *testing.T, address string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var value string
	var revision, receipts, events int
	err = conn.QueryRow(ctx, `SELECT convert_from(d.value,'UTF8'),i.revision,(SELECT count(*) FROM extension_p1_receipts),(SELECT count(*) FROM extension_p1_outbox) FROM extension_p1_data d JOIN extension_p1_instances i USING(installation,instance) WHERE d.key='note'`).Scan(&value, &revision, &receipts, &events)
	if err != nil || value != `{"value":1}` || revision != 1 || receipts != 1 || events != 3 {
		t.Fatal(value, revision, receipts, events, err)
	}
}

func retireBackupExtensionReceipt(t *testing.T, address string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `UPDATE extension_p1_receipts SET created_at=clock_timestamp()-interval '31 days'`)
	_ = conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count, err := postgres.RetireExtensionP1Receipts(ctx, address)
	if err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
