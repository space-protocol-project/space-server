package home

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type registryArchiveProbe struct {
	bindings                      map[string]postgres.ExtensionP1MutationBinding
	generation, registry, journal string
}

func TestExtensionP1RegistryArchiveRoundTrip(t *testing.T) {
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
			t.Run(name+suffix, func(t *testing.T) { extensionBackupRoundTrip(t, runtime, portable, retired, true) })
		}
	}
}

func newRegistryArchiveProbe(t *testing.T, store *postgres.Store, owner context.Context, token string, host *backupExtensionHost) (*registryArchiveProbe, *postgres.ExtensionP1ManagedService) {
	t.Helper()
	code, err := store.CreateSetupCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimOwner(owner, &pb.ClaimOwnerRequest{SetupCode: code}); err != nil {
		t.Fatal(err)
	}
	p := &registryArchiveProbe{bindings: backupExtensionBindings()}
	for _, state := range []string{"disabled", "disabling", "quarantined", "removed"} {
		b := p.bindings["demo"]
		b.Installation = state
		p.bindings[state] = b
	}
	s, err := store.NewExtensionP1ManagedService(context.Background(), host, p.bindings)
	if err != nil {
		t.Fatal(err)
	}
	p.generation = s.Generation()
	registryArchiveChange(t, s, token, "install", "enable", 1)
	registryArchiveChange(t, s, token, "disabling", "enable", 1)
	registryArchiveChange(t, s, token, "disabling", "disable", 2)
	if _, err = s.SystemTransition(context.Background(), "quarantined", "quarantine", 1); err != nil {
		t.Fatal(err)
	}
	registryArchiveChange(t, s, token, "removed", "remove", 1)
	return p, s
}

func registryArchiveChange(t *testing.T, s *postgres.ExtensionP1ManagedService, token, installation, action string, revision int64) {
	t.Helper()
	if _, err := s.Change(context.Background(), token, s.Generation(), installation, action, revision); err != nil {
		t.Fatal(err)
	}
}

func registryArchiveSnapshot(t *testing.T, address string) (string, string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var registry, journal string
	err = conn.QueryRow(ctx, `SELECT
 (SELECT jsonb_agg(to_jsonb(r)-'generation' ORDER BY installation)::text FROM extension_p1_installations r),
 (SELECT jsonb_agg(to_jsonb(a) ORDER BY installation,revision)::text FROM extension_p1_lifecycle_audit a)`).Scan(&registry, &journal)
	if err != nil {
		t.Fatal(err)
	}
	return registry, journal
}

func (p *registryArchiveProbe) capture(t *testing.T, address string) {
	p.registry, p.journal = registryArchiveSnapshot(t, address)
}

func (p *registryArchiveProbe) restore(t *testing.T, store *postgres.Store, address string, owner context.Context, token string, host *backupExtensionHost) *postgres.ExtensionP1ManagedService {
	t.Helper()
	p.checkSnapshot(t, address)
	conn, err := pgx.Connect(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	var renewed bool
	err = conn.QueryRow(context.Background(), `SELECT count(*)=5 AND bool_and(generation<>$1) FROM extension_p1_installations`, p.generation).Scan(&renewed)
	conn.Close(context.Background())
	if err != nil || !renewed {
		t.Fatal("Поколения архива не отозваны", renewed, err)
	}
	s, err := store.NewExtensionP1ManagedService(context.Background(), host, p.bindings)
	if err != nil {
		t.Fatal(err)
	}
	p.checkSnapshot(t, address)
	_, err = s.Change(context.Background(), token, p.generation, "install", "disable", 2)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatal("Старое поколение управления принято", err)
	}
	for _, instance := range []string{"disabled", "disabling", "quarantined", "removed"} {
		_, err = s.QueryExtension(owner, &pb.QueryExtensionRequest{InstanceId: instance, Query: "overview", PayloadJson: []byte(`{}`)})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatal(instance, err)
		}
	}
	if _, err = s.SystemTransition(context.Background(), "disabling", "finish_disable", 3); err != nil {
		t.Fatal(err)
	}
	checkRegistryArchiveAppend(t, address, s.Generation())
	if host.calls.Load() != 0 {
		t.Fatal("Восстановление запустило гостя")
	}
	t.Log("registry_states=5 audit_preserved=true generation_renewed=true pending_disable_completed=true")
	return s
}

func (p *registryArchiveProbe) checkSnapshot(t *testing.T, address string) {
	t.Helper()
	registry, journal := registryArchiveSnapshot(t, address)
	if registry != p.registry || journal != p.journal {
		t.Fatal("Архив изменил реестр или журнал")
	}
}

func checkRegistryArchiveAppend(t *testing.T, address, generation string) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var valid bool
	err = conn.QueryRow(context.Background(), `SELECT r.state='disabled' AND r.revision=4 AND a.actor='system' AND a.principal IS NULL AND a.action='finish_disable' AND a.generation=$1 AND (SELECT count(*) FROM extension_p1_lifecycle_audit)=11 FROM extension_p1_installations r JOIN extension_p1_lifecycle_audit a USING(installation,revision) WHERE r.installation='disabling'`, generation).Scan(&valid)
	if err != nil || !valid {
		t.Fatal(valid, err)
	}
	if err = postgres.CheckExtensionP1Backup(context.Background(), address, false); err != nil {
		t.Fatal(err)
	}
}
