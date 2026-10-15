package postgres

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	rotation "github.com/space-protocol-project/space-server/internal/identityrotation"
)

func TestRetentionSnapshotUsesCanonicalOrderAcrossCollations(t *testing.T) {
	ctx, databaseURL := isolatedDatabase(t)
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var supported bool
	if err = conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_collation WHERE collname='und-x-icu')").Scan(&supported); err != nil {
		t.Fatal(err)
	}
	if !supported {
		t.Skip("Нет ICU collation для проверки порядка")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, query := range []string{
		`CREATE TEMP TABLE principals(id text,auth_epoch bigint)`,
		`CREATE TEMP TABLE device_grants(id text COLLATE "und-x-icu",principal_id text,device_public_key bytea,scopes text[],expires_at timestamptz,revoked_at timestamptz,auth_epoch bigint,parent_grant_id text)`,
		`CREATE TEMP TABLE device_pairings(grant_id text,state text)`,
		`INSERT INTO principals VALUES('person',1)`,
	} {
		if _, err = tx.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{"dg_" + strings.Repeat("a", 43), "dg_" + strings.Repeat("A", 43), "dg_" + strings.Repeat("B", 43)}
	for _, id := range ids {
		_, err = tx.Exec(ctx, `INSERT INTO device_grants VALUES($1,'person',$2,ARRAY['chat.read','chat.write'],now()+interval '1 day',NULL,1,NULL)`, id, bytes.Repeat([]byte{7}, 32))
		if err != nil {
			t.Fatal(err)
		}
	}
	devices, err := retentionSnapshot(ctx, tx, "person", ids)
	if err != nil {
		t.Fatal(err)
	}
	expected := slices.Clone(ids)
	slices.Sort(expected)
	for i, id := range expected {
		if devices[i].GrantID != id {
			t.Fatal("Порядок Merkle-листьев зависит от collation PostgreSQL")
		}
	}
	if _, err = rotation.RetentionRoot(devices); err != nil {
		t.Fatal(err)
	}
}
