//go:build linux

package home

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
	"github.com/space-protocol-project/space-server/internal/postgres"
)

func runGroupArchive(t *testing.T, store *postgres.Store, host *extensions.ProcessHost, address, token, state string) {
	t.Helper()
	ctx := context.Background()
	if state == "resume" {
		svc, err := store.NewExtensionP1ManagedService(ctx, host, versionArchiveBindings())
		if err != nil {
			t.Fatal(err)
		}
		members := groupArchiveMembers(t, address)
		finishGroupArchive(t, svc, host, token, members, true)
		verifyGroupArchivePublished(t, address)
		repeatGroupArchive(t, address, host, token, members)
		return
	}
	for schema := uint32(1); schema <= 2; schema++ {
		active, identity, err := postgres.Open(ctx, address)
		if err != nil {
			t.Fatal(err)
		}
		defer active.Close()
		svc, err := active.NewExtensionP1ManagedService(ctx, host, versionArchiveBindings())
		if err != nil {
			t.Fatal(err)
		}
		members := planGroupArchive(t, address, identity.ServerID, svc.Generation(), schema)
		_, evidence := groupArchiveEvidence(t, members)
		if err = svc.PrepareGroupUpgrade(ctx, token, members, evidence); err != nil {
			t.Fatal(err)
		}
		if schema == 2 && state == "pending" {
			return
		}
		if schema == 2 && state == "partial" {
			digest, evidence := groupArchiveEvidence(t, members)
			if _, err = svc.AdvanceGroupUpgrade(ctx, token, "install", digest, "demo", 0, host, evidence["demo"]); err != nil {
				t.Fatal(err)
			}
			return
		}
		publish := schema == 1 || state == "published"
		finishGroupArchive(t, svc, host, token, members, publish)
	}
}

func planGroupArchive(t *testing.T, address, space, generation string, schema uint32) []postgres.ExtensionP1GroupMember {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	targetName := "upgraded"
	modules := []string{"migrate-ok", "migrate-empty"}
	if schema == 2 {
		targetName = "upgraded3"
		modules = []string{"migrate-next", "migrate-empty-next"}
	}
	var result []postgres.ExtensionP1GroupMember
	for i, id := range []string{"demo", "other"} {
		target := versionArchiveBindings()[id]
		target.Registration.Module = versionArchiveModule(t, targetName)
		raw, err := json.Marshal([]any{target.Registration.Module, target.Registration.Manifest, target.Events})
		if err != nil {
			t.Fatal(err)
		}
		module := versionArchiveModule(t, modules[i])
		p := upgrade.Plan{Version: upgrade.Version, Space: space, Installation: "install", Generation: generation, Grants: strings.Repeat("3", 64), Checkpoint: strings.Repeat("6", 64), SchemaFrom: schema, SchemaTo: schema + 1, Target: versionArchiveHash(raw), Steps: []upgrade.Step{{ID: "convert", From: schema, To: schema + 1, Digest: versionArchiveHash(module)}}}
		err = conn.QueryRow(ctx, `SELECT encode(i.package_hash,'hex'),i.revision,r.revision FROM extension_p1_instances i JOIN extension_p1_installations r USING(installation) WHERE i.instance=$1`, id).Scan(&p.Source, &p.DataRevision, &p.Revision)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, postgres.ExtensionP1GroupMember{Instance: id, Plan: p, Target: target, Modules: map[string][]byte{"convert": module}})
	}
	return result
}

func finishGroupArchive(t *testing.T, svc *postgres.ExtensionP1ManagedService, host *extensions.ProcessHost, token string, members []postgres.ExtensionP1GroupMember, publish bool) {
	t.Helper()
	ctx := context.Background()
	digest, evidence := groupArchiveEvidence(t, members)
	progress, err := svc.ReadGroupUpgrade(ctx, token, "install")
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Published {
		for _, m := range members {
			if progress.NextSteps[m.Instance] == len(m.Plan.Steps) {
				continue
			}
			if _, err = svc.AdvanceGroupUpgrade(ctx, token, "install", digest, m.Instance, 0, host, evidence[m.Instance]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !publish {
		return
	}
	if err = svc.PublishGroupUpgrade(ctx, token, "install", digest, evidence); err != nil {
		t.Fatal(err)
	}
}

func verifyGroupArchivePublished(t *testing.T, address string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var count int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM extension_p1_group_versions`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	if err = postgres.CheckExtensionP1Backup(ctx, address, false); err != nil {
		t.Fatal(err)
	}
}

func repeatGroupArchive(t *testing.T, address string, host *extensions.ProcessHost, token string, members []postgres.ExtensionP1GroupMember) {
	t.Helper()
	before := versionArchiveSnapshot(t, address)
	store, _, err := postgres.Open(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc, err := store.NewExtensionP1ManagedService(context.Background(), host, versionArchiveBindings())
	if err != nil {
		t.Fatal(err)
	}
	digest, evidence := groupArchiveEvidence(t, members)
	if err = svc.PublishGroupUpgrade(context.Background(), token, "install", digest, evidence); err != nil {
		t.Fatal("Повтор после открытия", err)
	}
	if !reflect.DeepEqual(before, versionArchiveSnapshot(t, address)) {
		t.Fatal("Повтор изменил историю или данные")
	}
}
