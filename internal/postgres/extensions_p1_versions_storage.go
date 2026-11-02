package postgres

import (
	"context"
	_ "embed"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

//go:embed extensions_p1_versions.sql
var extensionVersionsSchema string

func prepareVersionsStorage(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `ALTER TABLE extension_p1_meta ADD COLUMN IF NOT EXISTS versions_version integer NOT NULL DEFAULT 0 CHECK(versions_version IN (0,1))`); err != nil {
		return err
	}
	var version int
	if err := tx.QueryRow(ctx, `SELECT versions_version FROM extension_p1_meta WHERE singleton`).Scan(&version); err != nil {
		return err
	}
	if version == 0 {
		if _, err := tx.Exec(ctx, extensionVersionsSchema); err != nil {
			return err
		}
		if err := backfillVersions(ctx, tx); err != nil {
			return err
		}
	} else if version != 1 {
		return upgradeError()
	}
	return checkVersionHistory(ctx, tx)
}

func backfillVersions(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT installation FROM extension_p1_upgrade_jobs WHERE published ORDER BY installation`)
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, name := range names {
		p, instance, err := readUpgrade(ctx, tx, name)
		if err != nil {
			return err
		}
		if err = checkPublishedUpgrade(ctx, tx, p, instance); err != nil {
			return err
		}
		if err = archiveVersion(ctx, tx, p, instance); err != nil {
			return err
		}
	}
	return nil
}

// Вызывается в той же транзакции, что переключение рабочего пакета и аудит.
func archiveVersion(ctx context.Context, tx pgx.Tx, p ExtensionP1UpgradeProgress, instance string) error {
	r := versionRecord{Format: 1, Plan: p.Plan, Instance: instance, Document: p.Document, Steps: []versionStep{}}
	var err error
	r.Artifacts, err = readMigrationArtifacts(ctx, tx, p.Plan)
	if err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT seed,principal FROM extension_p1_upgrade_jobs WHERE installation=$1`, p.Plan.Installation).Scan(&r.Seed, &r.PreparedBy); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT principal,generation FROM extension_p1_lifecycle_audit WHERE installation=$1 AND revision=$2 AND action='publish_upgrade' AND actor='owner'`, p.Plan.Installation, p.Plan.Revision+1).Scan(&r.PublishedBy, &r.Generation); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT step_digest,input_digest,generation,principal,output,wasm FROM extension_p1_upgrade_steps WHERE installation=$1 ORDER BY ordinal`, p.Plan.Installation)
	if err != nil {
		return err
	}
	r.Steps, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (versionStep, error) {
		var s versionStep
		err := row.Scan(&s.Digest, &s.Input, &s.Generation, &s.Principal, &s.Output, &s.WASM)
		return s, err
	})
	if err != nil {
		return err
	}
	if err = r.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO extension_p1_versions(installation,sequence,digest,source,target,schema_from,schema_to,revision,principal,generation,record,record_hash)
 SELECT $1,COALESCE(max(sequence),0)+1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11 FROM extension_p1_versions WHERE installation=$1`, p.Plan.Installation, p.Digest, p.Plan.Source, p.Plan.Target, p.Plan.SchemaFrom, p.Plan.SchemaTo, p.Plan.Revision+1, r.PublishedBy, r.Generation, raw, upgradeHash(raw))
	return err
}

func readVersionHeads(ctx context.Context, tx pgx.Tx) (map[string]versionHead, error) {
	rows, err := tx.Query(ctx, `SELECT installation,sequence,digest,source,target,schema_from,schema_to,revision,principal,generation,record,record_hash FROM extension_p1_versions ORDER BY installation,sequence`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	heads := map[string]versionHead{}
	for rows.Next() {
		var name, hash string
		var raw []byte
		var h versionHead
		m := &h.Meta
		if err = rows.Scan(&name, &m.Sequence, &m.Digest, &m.Source, &m.Target, &m.SchemaFrom, &m.SchemaTo, &m.Revision, &m.Principal, &m.Generation, &raw, &hash); err != nil {
			return nil, err
		}
		h.Record, err = decodeVersionRecord(raw, hash)
		if err != nil {
			return nil, err
		}
		if h.Record.Plan.Installation != name {
			return nil, upgradeError()
		}
		var prior *versionHead
		if old, ok := heads[name]; ok {
			prior = &old
		}
		if err = validateVersionLink(prior, h); err != nil {
			return nil, err
		}
		heads[name] = h
	}
	return heads, rows.Err()
}

func currentPackageSchema(ctx context.Context, tx pgx.Tx, installation string) (uint32, error) {
	var version int
	if err := tx.QueryRow(ctx, `SELECT COALESCE((to_jsonb(m)->>'versions_version')::int,0) FROM extension_p1_meta m WHERE singleton`).Scan(&version); err != nil {
		return 0, err
	}
	if version == 0 {
		return 1, nil
	}
	if version != 1 {
		return 0, upgradeError()
	}
	var schema uint32
	err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT schema_to FROM extension_p1_versions WHERE installation=$1 ORDER BY sequence DESC LIMIT 1),1)`, installation).Scan(&schema)
	return schema, err
}

func prepareNextVersion(ctx context.Context, tx pgx.Tx, installation string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM extension_p1_upgrade_jobs WHERE installation=$1)`, installation).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	p, instance, err := readUpgrade(ctx, tx, installation)
	if err != nil {
		return err
	}
	if !p.Published {
		return upgradeError()
	}
	if err = checkPublishedUpgrade(ctx, tx, p, instance); err != nil {
		return err
	}
	// История уже зафиксирована при публикации. Удаляются только её рабочие копии.
	var digest string
	if err = tx.QueryRow(ctx, `SELECT digest FROM extension_p1_versions WHERE installation=$1 ORDER BY sequence DESC LIMIT 1`, installation).Scan(&digest); err != nil {
		return err
	}
	if digest != p.Digest {
		return upgradeError()
	}
	if err = checkVersionHistory(ctx, tx); err != nil {
		return err
	}
	for _, table := range []string{"extension_p1_upgrade_steps", "extension_p1_upgrade_artifacts", "extension_p1_upgrade_jobs"} {
		if _, err = tx.Exec(ctx, "DELETE FROM "+table+" WHERE installation=$1", installation); err != nil {
			return err
		}
	}
	return nil
}
