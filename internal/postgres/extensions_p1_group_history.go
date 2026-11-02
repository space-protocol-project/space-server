package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

type groupHead struct {
	Sequence int64
	Record   groupRecord
}

func readGroupHeads(ctx context.Context, tx pgx.Tx) (map[string]groupHead, error) {
	rows, err := tx.Query(ctx, `SELECT v.installation,v.sequence,v.digest,v.revision,v.principal,v.generation,v.record,v.record_hash,
 a.action,a.actor,a.principal,a.generation FROM extension_p1_group_versions v LEFT JOIN extension_p1_lifecycle_audit a USING(installation,revision) ORDER BY v.installation,v.sequence`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	heads := map[string]groupHead{}
	for rows.Next() {
		var name, digest, principal, generation, hash string
		var action, actor, author, auditGeneration *string
		var seq, revision int64
		var raw []byte
		if err = rows.Scan(&name, &seq, &digest, &revision, &principal, &generation, &raw, &hash, &action, &actor, &author, &auditGeneration); err != nil {
			return nil, err
		}
		if action == nil || *action != "publish_upgrade" || actor == nil || *actor != "owner" || author == nil || *author != principal || auditGeneration == nil || *auditGeneration != generation {
			return nil, upgradeError()
		}
		g, err := decodeGroup(raw, hash, true)
		if err != nil {
			return nil, err
		}
		if g.digest() != digest || g.Members[0].Plan.Installation != name {
			return nil, upgradeError()
		}
		old := heads[name]
		if seq != old.Sequence+1 {
			return nil, upgradeError()
		}
		if err = checkGroupLink(old, g, revision, principal, generation); err != nil {
			return nil, err
		}
		heads[name] = groupHead{Sequence: seq, Record: g}
	}
	return heads, rows.Err()
}

func checkGroupLink(old groupHead, g groupRecord, revision int64, principal, generation string) error {
	if old.Sequence != 0 && len(old.Record.Members) != len(g.Members) {
		return upgradeError()
	}
	for i, r := range g.Members {
		digest, _ := r.Plan.Digest()
		next := versionHead{Record: r, Meta: ExtensionP1Version{Sequence: old.Sequence + 1, Digest: digest, Source: r.Plan.Source, Target: r.Plan.Target, SchemaFrom: r.Plan.SchemaFrom, SchemaTo: r.Plan.SchemaTo, Revision: revision, Principal: principal, Generation: generation}}
		var previous *versionHead
		if old.Sequence != 0 {
			prior := old.Record.Members[i]
			previous = &versionHead{Record: prior, Meta: ExtensionP1Version{Sequence: old.Sequence, Revision: prior.Plan.Revision + 1}}
		}
		if err := validateVersionLink(previous, next); err != nil {
			return err
		}
	}
	return nil
}

func checkGroupStorage(ctx context.Context, tx pgx.Tx) error {
	heads, err := readGroupHeads(ctx, tx)
	if err != nil {
		return err
	}
	for _, h := range heads {
		if err = checkGroupHead(ctx, tx, h.Record); err != nil {
			return err
		}
	}
	rows, err := tx.Query(ctx, `SELECT installation FROM extension_p1_group_jobs ORDER BY installation`)
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, name := range names {
		g, published, err := readGroup(ctx, tx, name)
		if err != nil {
			return err
		}
		if err = groupMembership(ctx, tx, g); err != nil {
			return err
		}
		h := heads[name]
		if published {
			if h.Sequence == 0 || h.Record.digest() != g.digest() {
				return upgradeError()
			}
			expected, _ := json.Marshal(h.Record)
			actual, _ := json.Marshal(g)
			if upgradeHash(expected) != upgradeHash(actual) {
				return upgradeError()
			}
			continue
		}
		if err = checkPendingGroupLink(h, g); err != nil {
			return err
		}
		for _, r := range g.Members {
			if err = checkGroupSource(ctx, tx, r, true); err != nil {
				return err
			}
		}
	}
	var invalid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM extension_p1_group_versions g JOIN extension_p1_versions v USING(installation))
 OR EXISTS(SELECT 1 FROM extension_p1_group_jobs g JOIN extension_p1_upgrade_jobs j USING(installation))
 OR EXISTS(SELECT 1 FROM extension_p1_group_versions v LEFT JOIN extension_p1_group_jobs j USING(installation) WHERE j.installation IS NULL)`).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid {
		return upgradeError()
	}
	return nil
}

func checkPendingGroupLink(h groupHead, g groupRecord) error {
	if h.Sequence == 0 {
		if g.Members[0].Plan.SchemaFrom != 1 {
			return upgradeError()
		}
		return nil
	}
	if len(h.Record.Members) != len(g.Members) {
		return upgradeError()
	}
	for i, r := range g.Members {
		old := h.Record.Members[i]
		if r.Instance != old.Instance || r.Plan.Source != old.Plan.Target || r.Plan.SchemaFrom != old.Plan.SchemaTo || r.Plan.Space != old.Plan.Space || r.Plan.Revision < old.Plan.Revision+1 || r.Plan.DataRevision < old.Plan.DataRevision || r.Artifacts.Target.Channel != old.Artifacts.Target.Channel {
			return upgradeError()
		}
	}
	return nil
}

func checkGroupBackup(ctx context.Context, tx pgx.Tx, tables map[string]bool) error {
	version, err := groupStorageVersion(ctx, tx)
	if err != nil {
		return err
	}
	jobs, versions := tables["extension_p1_group_jobs"], tables["extension_p1_group_versions"]
	if version == 0 && !jobs && !versions {
		return nil
	}
	if version != 1 || !jobs || !versions || !tables["extension_p1_versions"] {
		return upgradeError()
	}
	return checkGroupStorage(ctx, tx)
}

func groupPublishedBindings(ctx context.Context, tx pgx.Tx, result map[string]ExtensionP1MutationBinding) error {
	version, err := groupStorageVersion(ctx, tx)
	if err != nil || version == 0 {
		return err
	}
	if version != 1 {
		return upgradeError()
	}
	heads, err := readGroupHeads(ctx, tx)
	if err != nil {
		return err
	}
	for name, h := range heads {
		count := 0
		for _, b := range result {
			if b.Installation == name {
				count++
			}
		}
		if count != len(h.Record.Members) {
			return upgradeError()
		}
		for _, r := range h.Record.Members {
			b, ok := result[r.Instance]
			if !ok || b.Installation != name || b.Channel != r.Artifacts.Target.Channel {
				return upgradeError()
			}
			hash := mutationPackageHash(b)
			var known bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM extension_p1_group_versions WHERE installation=$1 AND (convert_from(record,'UTF8')::jsonb->'Members'->0->'Plan'->>'source'=$2 OR convert_from(record,'UTF8')::jsonb->'Members'->0->'Plan'->>'target'=$2))`, name, hex.EncodeToString(hash[:])).Scan(&known); err != nil {
				return err
			}
			if !known {
				return upgradeError()
			}
			result[r.Instance] = r.Artifacts.Target
		}
	}
	return nil
}
