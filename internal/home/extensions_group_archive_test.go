package home

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
	"github.com/space-protocol-project/space-server/internal/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func versionArchiveGroup() bool { return os.Getenv("SPACE_P1_ARCHIVE_GROUP") == "1" }
func versionArchiveBindings() map[string]postgres.ExtensionP1MutationBinding {
	b := backupExtensionBindings()
	if versionArchiveGroup() {
		b["other"] = b["demo"]
	}
	return b
}

func checkGroupArchive(t *testing.T, address string, svc *postgres.ExtensionP1ManagedService, token string, m versionArchiveFixture) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var versions, instances int
	var revision int64
	err = conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM extension_p1_group_versions),(SELECT count(*) FROM extension_p1_instances),revision FROM extension_p1_installations WHERE installation='install'`).Scan(&versions, &instances, &revision)
	want := 1
	if m.State == "published" {
		want = 2
	}
	if err != nil || versions != want || instances != 2 {
		t.Fatal(versions, instances, err)
	}
	p, err := svc.ReadGroupUpgrade(ctx, token, "install")
	if err != nil || p.Published != (m.State == "published") || p.Ready != (m.State == "ready" || m.State == "published") || len(p.NextSteps) != 2 {
		t.Fatal(p, err)
	}
	for _, id := range []string{"demo", "other"} {
		step, ok := p.NextSteps[id]
		want := 1
		if m.State == "pending" || (m.State == "partial" && id == "other") {
			want = 0
		}
		if !ok || step != want {
			t.Fatal(id, step)
		}
	}
	if _, err = svc.Change(ctx, token, m.Registry.Generation, "install", "enable", revision); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("Принято старое поколение", err)
	}
}

func groupArchiveMembers(t *testing.T, address string) []postgres.ExtensionP1GroupMember {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var raw []byte
	if err = conn.QueryRow(ctx, `SELECT record FROM extension_p1_group_jobs WHERE installation='install'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record struct {
		Members []struct {
			Instance  string
			Plan      upgrade.Plan
			Artifacts struct {
				Target  postgres.ExtensionP1MutationBinding
				Modules map[string][]byte
			}
		}
	}
	if err = json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	var result []postgres.ExtensionP1GroupMember
	for _, m := range record.Members {
		result = append(result, postgres.ExtensionP1GroupMember{Instance: m.Instance, Plan: m.Plan, Target: m.Artifacts.Target, Modules: m.Artifacts.Modules})
	}
	if len(result) != 2 {
		t.Fatal("Неполный состав группы")
	}
	return result
}

func groupArchiveEvidence(t *testing.T, members []postgres.ExtensionP1GroupMember) (string, map[string]postgres.ExtensionP1GroupEvidence) {
	t.Helper()
	digest, err := postgres.ExtensionP1GroupDigest(members)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]postgres.ExtensionP1GroupEvidence{}
	for _, m := range members {
		p := m.Plan
		d, err := p.Digest()
		if err != nil {
			t.Fatal(err)
		}
		result[m.Instance] = postgres.ExtensionP1GroupEvidence{Digest: digest, Verification: upgrade.Verification{Plan: d, Target: p.Target, Grants: p.Grants, Trusted: true, Compatible: true, Approved: true}, Checkpoint: upgrade.Checkpoint{Plan: d, Digest: p.Checkpoint, Source: p.Source, Schema: p.SchemaFrom, DataRevision: p.DataRevision, Complete: true}}
	}
	return digest, result
}

func corruptGroupArchiveModule(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	var raw []byte
	if err := conn.QueryRow(ctx, `SELECT record FROM extension_p1_group_jobs WHERE installation='install'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	member := record["Members"].([]any)[1].(map[string]any)
	artifacts := member["Artifacts"].(map[string]any)
	artifacts["Modules"].(map[string]any)["convert"] = "AA=="
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	if _, err = conn.Exec(ctx, `UPDATE extension_p1_group_jobs SET record=$1,record_hash=$2 WHERE installation='install'`, raw, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
}
