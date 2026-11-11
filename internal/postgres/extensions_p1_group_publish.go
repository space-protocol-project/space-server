package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/space-protocol-project/space-server/experiments/extensions/lifecycle"
)

// PublishGroupUpgrade переключает данные всех экземпляров, пакеты, историю,
// аудит и поколение в одной транзакции. Старая оболочка после этого закрыта.
func (s *ExtensionP1ManagedService) PublishGroupUpgrade(ctx context.Context, token, installation, digest string, evidence map[string]ExtensionP1GroupEvidence) error {
	changed := false
	err := s.upgradeTransaction(ctx, token, installation, func(ctx context.Context, tx pgx.Tx, principal string) error {
		if err := checkGroupStorage(ctx, tx); err != nil {
			return err
		}
		g, published, err := readGroup(ctx, tx, installation)
		if err != nil {
			return err
		}
		if digest != g.digest() || !g.progress(published).Ready {
			return upgradeError()
		}
		if published {
			return checkGroupHead(ctx, tx, g)
		}
		if len(evidence) != len(g.Members) {
			return upgradeError()
		}
		for _, r := range g.Members {
			if err = s.attestGroupMember(ctx, tx, g, r, evidence[r.Instance], true); err != nil {
				return err
			}
			if err = checkGroupSource(ctx, tx, r, false); err != nil {
				return err
			}
		}
		for i := range g.Members {
			r := &g.Members[i]
			if err = publishGroupInstance(ctx, tx, *r); err != nil {
				return err
			}
			r.PublishedBy = principal
			r.Generation = s.generation
		}
		if err = s.publishGroupRegistry(ctx, tx, g, principal); err != nil {
			return err
		}
		if err = saveGroup(ctx, tx, g, true); err != nil {
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

func publishGroupInstance(ctx context.Context, tx pgx.Tx, r versionRecord) error {
	data, err := decodeMigrationData(r.Document, r.Plan.SchemaTo)
	if err != nil {
		return err
	}
	if err = writeMigrationData(ctx, tx, r.Plan.Installation, r.Instance, data); err != nil {
		return err
	}
	hash := mutationPackageHash(r.Artifacts.Target)
	_, err = tx.Exec(ctx, `UPDATE extension_p1_instances SET package_hash=$3 WHERE installation=$1 AND instance=$2`, r.Plan.Installation, r.Instance, hash[:])
	return err
}

func (s *ExtensionP1ManagedService) publishGroupRegistry(ctx context.Context, tx pgx.Tx, g groupRecord, principal string) error {
	p := g.Members[0].Plan
	binding := lifecycleBindingHash(groupBindings(g), p.Installation)
	d := lifecycle.Decision{Code: "OK", State: "disabled", Revision: p.Revision + 1}
	if err := s.control.recordTransition(ctx, tx, p.Installation, "publish_upgrade", "owner", principal, p.Revision, d); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE extension_p1_installations SET binding_hash=$2,revision=$3,generation=replace(gen_random_uuid()::text,'-','') WHERE installation=$1`, p.Installation, binding[:], d.Revision); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE extension_p1_meta SET history_id=replace(gen_random_uuid()::text,'-','') WHERE singleton`); err != nil {
		return err
	}
	if err := g.validate(true); err != nil {
		return err
	}
	raw, err := json.Marshal(g)
	if err != nil {
		return err
	}
	if err = admitHistory(ctx, tx, p.Installation, len(raw)); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO extension_p1_group_versions(installation,sequence,digest,revision,principal,generation,record,record_hash)
 SELECT $1,COALESCE(max(sequence),0)+1,$2,$3,$4,$5,$6,$7 FROM extension_p1_group_versions WHERE installation=$1`, p.Installation, g.digest(), d.Revision, principal, s.generation, raw, upgradeHash(raw))
	return err
}
