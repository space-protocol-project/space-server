package postgres

import (
	"context"
	"encoding/hex"

	"github.com/jackc/pgx/v5"
	"github.com/space-protocol-project/space-server/experiments/extensions/lifecycle"
	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
)

// PublishUpgrade меняет пакет, данные, поколение, историю команд и аудит
// одним COMMIT. Старая оболочка закрывается; новая остаётся disabled.
func (s *ExtensionP1ManagedService) PublishUpgrade(ctx context.Context, token, installation, digest string, v upgrade.Verification, c upgrade.Checkpoint) error {
	changed := false
	err := s.upgradeTransaction(ctx, token, installation, func(ctx context.Context, tx pgx.Tx, principal string) error {
		if err := checkVersionHistory(ctx, tx); err != nil {
			return err
		}
		p, instance, err := readUpgrade(ctx, tx, installation)
		if err != nil {
			return err
		}
		if p.Digest != digest || !p.Ready() {
			return upgradeError()
		}
		if p.Published {
			return checkPublishedUpgrade(ctx, tx, p, instance)
		}
		b, err := upgradeBinding(ctx, tx, p.Plan, instance)
		if err != nil {
			return err
		}
		b.Generation = p.Plan.Generation
		if _, err = upgrade.Begin(p.Plan, b, v, c, true); err != nil {
			return err
		}
		a, err := readMigrationArtifacts(ctx, tx, p.Plan)
		if err != nil {
			return err
		}
		data, err := decodeMigrationData(p.Document, p.Plan.SchemaTo)
		if err != nil {
			return err
		}
		if err = checkWASMMigration(ctx, tx, p, instance); err != nil {
			return err
		}
		if err = writeMigrationData(ctx, tx, installation, instance, data); err != nil {
			return err
		}
		if err = s.control.publishBinding(ctx, tx, p, instance, a.Target, principal); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err == nil && changed {
		s.control.installations[installation].failed.Store(true)
	}
	return err
}

func checkWASMMigration(ctx context.Context, tx pgx.Tx, p ExtensionP1UpgradeProgress, instance string) error {
	var seed []byte
	var unsafe bool
	err := tx.QueryRow(ctx, `SELECT seed,EXISTS(SELECT 1 FROM extension_p1_upgrade_steps s WHERE s.installation=j.installation AND NOT s.wasm) FROM extension_p1_upgrade_jobs j WHERE installation=$1`, p.Plan.Installation).Scan(&seed, &unsafe)
	if err != nil {
		return err
	}
	if unsafe {
		return upgradeError()
	}
	current, err := readMigrationData(ctx, tx, p.Plan.Installation, instance, p.Plan.SchemaFrom)
	if err != nil {
		return err
	}
	if upgradeHash(current) != upgradeHash(seed) {
		return upgradeError()
	}
	return nil
}

func (c *extensionLifecycle) publishBinding(ctx context.Context, tx pgx.Tx, p ExtensionP1UpgradeProgress, instance string, target ExtensionP1MutationBinding, principal string) error {
	hash := mutationPackageHash(target)
	if _, err := tx.Exec(ctx, `UPDATE extension_p1_instances SET package_hash=$3 WHERE installation=$1 AND instance=$2`, p.Plan.Installation, instance, hash[:]); err != nil {
		return err
	}
	bindings := map[string]extensionMutationBinding{instance: {installation: p.Plan.Installation, channel: target.Channel, packageHash: hash}}
	bindingHash := lifecycleBindingHash(bindings, p.Plan.Installation)
	d := lifecycle.Decision{Code: "OK", State: "disabled", Revision: p.Plan.Revision + 1}
	if err := c.recordTransition(ctx, tx, p.Plan.Installation, "publish_upgrade", "owner", principal, p.Plan.Revision, d); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE extension_p1_installations SET binding_hash=$2,revision=$3,generation=replace(gen_random_uuid()::text,'-','') WHERE installation=$1`, p.Plan.Installation, bindingHash[:], d.Revision); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE extension_p1_meta SET history_id=replace(gen_random_uuid()::text,'-','') WHERE singleton`); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE extension_p1_upgrade_jobs SET published=true,result_digest=$2 WHERE installation=$1`, p.Plan.Installation, upgradeHash(p.Document))
	if err != nil {
		return err
	}
	return archiveVersion(ctx, tx, p, instance)
}

func checkPublishedUpgrade(ctx context.Context, tx pgx.Tx, p ExtensionP1UpgradeProgress, instance string) error {
	if !p.Ready() {
		return upgradeError()
	}
	a, err := readMigrationArtifacts(ctx, tx, p.Plan)
	if err != nil {
		return err
	}
	var result, source, space string
	var revision int64
	var dataRevision uint64
	var binding []byte
	err = tx.QueryRow(ctx, `SELECT j.result_digest,encode(i.package_hash,'hex'),s.server_id,r.revision,i.revision,r.binding_hash
 FROM extension_p1_upgrade_jobs j JOIN extension_p1_installations r USING(installation) JOIN extension_p1_instances i USING(installation) CROSS JOIN server_state s
 WHERE j.installation=$1 AND i.instance=$2 AND s.singleton`, p.Plan.Installation, instance).Scan(&result, &source, &space, &revision, &dataRevision, &binding)
	if err != nil {
		return err
	}
	hash := mutationPackageHash(a.Target)
	want := lifecycleBindingHash(map[string]extensionMutationBinding{instance: {installation: p.Plan.Installation, channel: a.Target.Channel, packageHash: hash}}, p.Plan.Installation)
	if result != upgradeHash(p.Document) || source != p.Plan.Target || space != p.Plan.Space || revision < p.Plan.Revision+1 || dataRevision < p.Plan.DataRevision || hex.EncodeToString(binding) != hex.EncodeToString(want[:]) {
		return upgradeError()
	}
	var unsafe bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM extension_p1_upgrade_steps WHERE installation=$1 AND NOT wasm)`, p.Plan.Installation).Scan(&unsafe); err != nil {
		return err
	}
	if unsafe {
		return upgradeError()
	}
	if dataRevision == p.Plan.DataRevision {
		current, err := readMigrationData(ctx, tx, p.Plan.Installation, instance, p.Plan.SchemaTo)
		if err != nil {
			return err
		}
		if upgradeHash(current) != result {
			return upgradeError()
		}
	}
	return nil
}
