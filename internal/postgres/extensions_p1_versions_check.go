package postgres

import (
	"context"
	"encoding/hex"

	"github.com/jackc/pgx/v5"
)

func checkVersionHistory(ctx context.Context, tx pgx.Tx) error {
	heads, err := readVersionHeads(ctx, tx)
	if err != nil {
		return err
	}
	var invalid bool
	query := `SELECT EXISTS(SELECT 1 FROM extension_p1_versions v LEFT JOIN extension_p1_lifecycle_audit a USING(installation,revision)
 WHERE a.installation IS NULL OR a.action<>'publish_upgrade' OR a.actor<>'owner' OR a.principal IS DISTINCT FROM v.principal OR a.generation<>v.generation)
 OR EXISTS(SELECT 1 FROM extension_p1_lifecycle_audit a LEFT JOIN extension_p1_versions v USING(installation,revision)
 WHERE a.action='publish_upgrade' AND v.installation IS NULL`
	version, err := groupStorageVersion(ctx, tx)
	if err != nil {
		return err
	}
	if version == 1 {
		query += ` AND NOT EXISTS(SELECT 1 FROM extension_p1_group_versions g WHERE g.installation=a.installation AND g.revision=a.revision)`
	}
	err = tx.QueryRow(ctx, query+`)`).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid {
		return upgradeError()
	}
	for _, head := range heads {
		if err = checkVersionHead(ctx, tx, head); err != nil {
			return err
		}
	}
	return nil
}

func checkVersionHead(ctx context.Context, tx pgx.Tx, h versionHead) error {
	r := h.Record
	var space, hash, channel string
	var revision int64
	var dataRevision uint64
	var binding []byte
	err := tx.QueryRow(ctx, `SELECT s.server_id,encode(i.package_hash,'hex'),i.channel,i.revision,r.revision,r.binding_hash
 FROM extension_p1_installations r JOIN extension_p1_instances i USING(installation) CROSS JOIN server_state s
 WHERE r.installation=$1 AND i.instance=$2 AND s.singleton`, r.Plan.Installation, r.Instance).Scan(&space, &hash, &channel, &dataRevision, &revision, &binding)
	if err != nil {
		return err
	}
	targetHash := mutationPackageHash(r.Artifacts.Target)
	wanted := lifecycleBindingHash(map[string]extensionMutationBinding{r.Instance: {installation: r.Plan.Installation, channel: r.Artifacts.Target.Channel, packageHash: targetHash}}, r.Plan.Installation)
	if space != r.Plan.Space || hash != r.Plan.Target || channel != r.Artifacts.Target.Channel || dataRevision < r.Plan.DataRevision || revision < h.Meta.Revision || hex.EncodeToString(binding) != hex.EncodeToString(wanted[:]) {
		return upgradeError()
	}
	if dataRevision == r.Plan.DataRevision {
		raw, err := readMigrationData(ctx, tx, r.Plan.Installation, r.Instance, r.Plan.SchemaTo)
		if err != nil {
			return err
		}
		if upgradeHash(raw) != upgradeHash(r.Document) {
			return upgradeError()
		}
	}
	return nil
}

func checkVersionsBackup(ctx context.Context, tx pgx.Tx, tables map[string]bool) error {
	var version int
	if err := tx.QueryRow(ctx, `SELECT COALESCE((to_jsonb(m)->>'versions_version')::int,0) FROM extension_p1_meta m WHERE singleton`).Scan(&version); err != nil {
		return err
	}
	exists := tables["extension_p1_versions"]
	if version == 0 && !exists {
		return nil
	}
	if version != 1 || !exists || !tables["extension_p1_upgrade_artifacts"] {
		return upgradeError()
	}
	return checkVersionHistory(ctx, tx)
}

func (s *ExtensionP1ManagedService) ListUpgradeVersions(ctx context.Context, token, installation string, after int64, limit int) ([]ExtensionP1Version, error) {
	if after < 0 || limit < 1 || limit > 100 {
		return nil, upgradeError()
	}
	result := []ExtensionP1Version{}
	err := s.upgradeTransaction(ctx, token, installation, func(ctx context.Context, tx pgx.Tx, _ string) error {
		rows, err := tx.Query(ctx, `SELECT sequence,digest,source,target,schema_from,schema_to,revision,principal,generation FROM (
 SELECT installation,sequence,digest,source,target,schema_from,schema_to,revision,principal,generation FROM extension_p1_versions
 UNION ALL SELECT installation,sequence,digest,
 convert_from(record,'UTF8')::jsonb->'Members'->0->'Plan'->>'source',
 convert_from(record,'UTF8')::jsonb->'Members'->0->'Plan'->>'target',
 (convert_from(record,'UTF8')::jsonb->'Members'->0->'Plan'->>'schema_from')::integer,
 (convert_from(record,'UTF8')::jsonb->'Members'->0->'Plan'->>'schema_to')::integer,
 revision,principal,generation FROM extension_p1_group_versions
 ) history WHERE installation=$1 AND sequence>$2 ORDER BY sequence LIMIT $3`, installation, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v ExtensionP1Version
			if err = rows.Scan(&v.Sequence, &v.Digest, &v.Source, &v.Target, &v.SchemaFrom, &v.SchemaTo, &v.Revision, &v.Principal, &v.Generation); err != nil {
				return err
			}
			result = append(result, v)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) versionBindings(ctx context.Context, bindings map[string]ExtensionP1MutationBinding) (map[string]ExtensionP1MutationBinding, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	heads, err := readVersionHeads(ctx, tx)
	if err != nil {
		return nil, err
	}
	result := make(map[string]ExtensionP1MutationBinding, len(bindings))
	for id, b := range bindings {
		result[id] = b
	}
	for installation, h := range heads {
		r := h.Record
		b, ok := result[r.Instance]
		if !ok || b.Installation != installation || b.Channel != r.Artifacts.Target.Channel {
			return nil, upgradeError()
		}
		hash := mutationPackageHash(b)
		var known bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM extension_p1_versions WHERE installation=$1 AND (source=$2 OR target=$2))`, installation, hex.EncodeToString(hash[:])).Scan(&known); err != nil {
			return nil, err
		}
		if !known {
			return nil, upgradeError()
		}
		result[r.Instance] = r.Artifacts.Target
	}
	if err = groupPublishedBindings(ctx, tx, result); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}
