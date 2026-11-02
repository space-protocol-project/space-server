package postgres

import (
	"bytes"
	"context"

	"github.com/jackc/pgx/v5"
	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
)

func prepareUpgradeStorage(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `ALTER TABLE extension_p1_meta ADD COLUMN IF NOT EXISTS upgrade_version integer NOT NULL DEFAULT 0 CHECK(upgrade_version IN (0,1))`); err != nil {
		return err
	}
	var version int
	if err := tx.QueryRow(ctx, `SELECT upgrade_version FROM extension_p1_meta WHERE singleton`).Scan(&version); err != nil {
		return err
	}
	if version == 0 {
		_, err := tx.Exec(ctx, extensionUpgradeSchema)
		return err
	}
	if version != 1 {
		return upgradeError()
	}
	return checkUpgradeJobs(ctx, tx)
}

func readUpgrade(ctx context.Context, tx pgx.Tx, installation string) (ExtensionP1UpgradeProgress, string, error) {
	var p ExtensionP1UpgradeProgress
	var raw, seed []byte
	var instance string
	err := tx.QueryRow(ctx, `SELECT plan,digest,instance,seed,document,next_step FROM extension_p1_upgrade_jobs WHERE installation=$1`, installation).Scan(&raw, &p.Digest, &instance, &seed, &p.Document, &p.NextStep)
	if err != nil {
		return p, instance, err
	}
	p.Plan, err = upgrade.Decode(raw)
	if err != nil {
		return p, instance, err
	}
	digest, _ := p.Plan.Digest()
	if digest != p.Digest || p.Plan.Installation != installation || p.NextStep < 0 || p.NextStep > len(p.Plan.Steps) || extensions.CheckJSON(seed) != nil {
		return p, instance, upgradeError()
	}
	err = checkUpgradeSteps(ctx, tx, p, seed)
	return p, instance, err
}

func checkUpgradeSteps(ctx context.Context, tx pgx.Tx, p ExtensionP1UpgradeProgress, seed []byte) error {
	rows, err := tx.Query(ctx, `SELECT ordinal,step_digest,input_digest,output FROM extension_p1_upgrade_steps WHERE installation=$1 ORDER BY ordinal`, p.Plan.Installation)
	if err != nil {
		return err
	}
	defer rows.Close()
	next := 0
	previous := seed
	for rows.Next() {
		var ordinal int
		var step, input string
		var output []byte
		if err = rows.Scan(&ordinal, &step, &input, &output); err != nil {
			return err
		}
		if next >= p.NextStep || ordinal != next || step != p.Plan.Steps[next].Digest || input != upgradeHash(previous) || extensions.CheckJSON(output) != nil {
			return upgradeError()
		}
		previous = output
		next++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if next != p.NextStep || !bytes.Equal(previous, p.Document) {
		return upgradeError()
	}
	return nil
}

func checkUpgradeJobs(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT j.installation FROM extension_p1_upgrade_jobs j JOIN extension_p1_installations r USING(installation) ORDER BY j.installation FOR UPDATE OF r`)
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
		var state, space, source string
		var revision int64
		var dataRevision uint64
		err = tx.QueryRow(ctx, `SELECT r.state,r.revision,s.server_id,encode(i.package_hash,'hex'),i.revision FROM extension_p1_installations r JOIN extension_p1_instances i USING(installation) CROSS JOIN server_state s WHERE r.installation=$1 AND i.instance=$2 AND s.singleton`, name, instance).Scan(&state, &revision, &space, &source, &dataRevision)
		if err != nil {
			return err
		}
		// Карантин останавливает выполнение, но сохраняет пригодный для разбора журнал.
		validState := (state == "disabled" && revision == p.Plan.Revision) || (state == "quarantined" && revision == p.Plan.Revision+1)
		if !validState || space != p.Plan.Space || source != p.Plan.Source || dataRevision != p.Plan.DataRevision {
			return upgradeError()
		}
	}
	return nil
}

func checkUpgradeBackup(ctx context.Context, tx pgx.Tx, tables map[string]bool) error {
	var version int
	if err := tx.QueryRow(ctx, `SELECT COALESCE((to_jsonb(m)->>'upgrade_version')::int,0) FROM extension_p1_meta m WHERE singleton`).Scan(&version); err != nil {
		return err
	}
	jobs, steps := tables["extension_p1_upgrade_jobs"], tables["extension_p1_upgrade_steps"]
	if version == 0 && !jobs && !steps {
		return nil
	}
	if version != 1 || !jobs || !steps || !tables["extension_p1_installations"] {
		return upgradeError()
	}
	return checkUpgradeJobs(ctx, tx)
}

func blockUpgradeChange(ctx context.Context, tx pgx.Tx, installation string) error {
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM extension_p1_upgrade_jobs WHERE installation=$1)`, installation).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return upgradeError()
	}
	return nil
}
