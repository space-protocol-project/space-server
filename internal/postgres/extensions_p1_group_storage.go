package postgres

import (
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
)

//go:embed extensions_p1_group.sql
var extensionGroupSchema string

func groupStorageVersion(ctx context.Context, tx pgx.Tx) (int, error) {
	var version int
	err := tx.QueryRow(ctx, `SELECT COALESCE((to_jsonb(m)->>'group_version')::int,0) FROM extension_p1_meta m WHERE singleton`).Scan(&version)
	return version, err
}

func prepareGroupStorage(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `ALTER TABLE extension_p1_meta ADD COLUMN IF NOT EXISTS group_version integer NOT NULL DEFAULT 0 CHECK(group_version IN (0,1))`); err != nil {
		return err
	}
	version, err := groupStorageVersion(ctx, tx)
	if err != nil {
		return err
	}
	if version == 0 {
		_, err = tx.Exec(ctx, extensionGroupSchema)
		return err
	}
	if version != 1 {
		return upgradeError()
	}
	return checkGroupStorage(ctx, tx)
}

func readGroup(ctx context.Context, tx pgx.Tx, installation string) (groupRecord, bool, error) {
	var raw []byte
	var hash, digest string
	var published bool
	err := tx.QueryRow(ctx, `SELECT record,record_hash,digest,published FROM extension_p1_group_jobs WHERE installation=$1`, installation).Scan(&raw, &hash, &digest, &published)
	if err != nil {
		return groupRecord{}, false, err
	}
	g, err := decodeGroup(raw, hash, published)
	if err != nil {
		return g, published, err
	}
	if g.digest() != digest || g.Members[0].Plan.Installation != installation {
		return g, published, upgradeError()
	}
	return g, published, nil
}

func saveGroup(ctx context.Context, tx pgx.Tx, g groupRecord, published bool) error {
	if err := g.validate(published); err != nil {
		return err
	}
	raw, err := json.Marshal(g)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE extension_p1_group_jobs SET record=$2,record_hash=$3,published=$4 WHERE installation=$1`, g.Members[0].Plan.Installation, raw, upgradeHash(raw), published)
	return err
}

func groupMembership(ctx context.Context, tx pgx.Tx, g groupRecord) error {
	rows, err := tx.Query(ctx, `SELECT instance,channel FROM extension_p1_instances WHERE installation=$1 ORDER BY instance`, g.Members[0].Plan.Installation)
	if err != nil {
		return err
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var id, channel string
		if err = rows.Scan(&id, &channel); err != nil {
			return err
		}
		if i >= len(g.Members) || id != g.Members[i].Instance || channel != g.Members[i].Artifacts.Target.Channel {
			return upgradeError()
		}
		i++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if i != len(g.Members) {
		return upgradeError()
	}
	return nil
}

func groupBinding(ctx context.Context, tx pgx.Tx, r versionRecord, allowQuarantine bool) (upgrade.Binding, error) {
	var b upgrade.Binding
	var state string
	err := tx.QueryRow(ctx, `SELECT s.server_id,i.installation,r.generation,r.revision,r.state,encode(i.package_hash,'hex'),i.revision
 FROM extension_p1_instances i JOIN extension_p1_installations r USING(installation) CROSS JOIN server_state s
 WHERE i.installation=$1 AND i.instance=$2 AND s.singleton`, r.Plan.Installation, r.Instance).Scan(&b.Space, &b.Installation, &b.Generation, &b.Revision, &state, &b.Package, &b.DataRevision)
	if err != nil {
		return b, err
	}
	if allowQuarantine && state == "quarantined" && b.Revision == r.Plan.Revision+1 {
		b.Revision--
	} else if state != "disabled" {
		return b, upgradeError()
	}
	err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT (convert_from(record,'UTF8')::jsonb->'Members'->0->'Plan'->>'schema_to')::integer FROM extension_p1_group_versions WHERE installation=$1 ORDER BY sequence DESC LIMIT 1),1)`, r.Plan.Installation).Scan(&b.Schema)
	return b, err
}

func checkGroupSource(ctx context.Context, tx pgx.Tx, r versionRecord, allowQuarantine bool) error {
	b, err := groupBinding(ctx, tx, r, allowQuarantine)
	if err != nil {
		return err
	}
	p := r.Plan
	if b.Space != p.Space || b.Installation != p.Installation || b.Revision != p.Revision || b.Package != p.Source || b.DataRevision != p.DataRevision || b.Schema != p.SchemaFrom {
		return upgradeError()
	}
	raw, err := readMigrationData(ctx, tx, p.Installation, r.Instance, p.SchemaFrom)
	if err != nil {
		return err
	}
	if upgradeHash(raw) != upgradeHash(r.Seed) {
		return upgradeError()
	}
	return nil
}

func groupBindings(g groupRecord) map[string]extensionMutationBinding {
	result := map[string]extensionMutationBinding{}
	for _, r := range g.Members {
		result[r.Instance] = extensionMutationBinding{installation: r.Plan.Installation, channel: r.Artifacts.Target.Channel, packageHash: mutationPackageHash(r.Artifacts.Target)}
	}
	return result
}

func checkGroupHead(ctx context.Context, tx pgx.Tx, g groupRecord) error {
	if err := groupMembership(ctx, tx, g); err != nil {
		return err
	}
	first := g.Members[0].Plan
	var binding []byte
	var revision int64
	var space string
	if err := tx.QueryRow(ctx, `SELECT r.binding_hash,r.revision,s.server_id FROM extension_p1_installations r CROSS JOIN server_state s WHERE r.installation=$1 AND s.singleton`, first.Installation).Scan(&binding, &revision, &space); err != nil {
		return err
	}
	wanted := lifecycleBindingHash(groupBindings(g), first.Installation)
	if space != first.Space || revision < first.Revision+1 || hex.EncodeToString(binding) != hex.EncodeToString(wanted[:]) {
		return upgradeError()
	}
	for _, r := range g.Members {
		if err := checkGroupInstance(ctx, tx, r); err != nil {
			return err
		}
	}
	return nil
}

func checkGroupInstance(ctx context.Context, tx pgx.Tx, r versionRecord) error {
	var hash string
	var revision uint64
	if err := tx.QueryRow(ctx, `SELECT encode(package_hash,'hex'),revision FROM extension_p1_instances WHERE installation=$1 AND instance=$2`, r.Plan.Installation, r.Instance).Scan(&hash, &revision); err != nil {
		return err
	}
	if hash != r.Plan.Target || revision < r.Plan.DataRevision {
		return upgradeError()
	}
	if revision == r.Plan.DataRevision {
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
