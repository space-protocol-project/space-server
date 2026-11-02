package postgres

import (
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
)

//go:embed extensions_p1_publish.sql
var extensionPublishSchema string

type migrationArtifacts struct {
	Target  ExtensionP1MutationBinding
	Modules map[string][]byte
}

func preparePublishStorage(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `ALTER TABLE extension_p1_meta ADD COLUMN IF NOT EXISTS publish_version integer NOT NULL DEFAULT 0 CHECK(publish_version IN (0,1))`); err != nil {
		return err
	}
	var version int
	if err := tx.QueryRow(ctx, `SELECT publish_version FROM extension_p1_meta WHERE singleton`).Scan(&version); err != nil {
		return err
	}
	if version == 0 {
		_, err := tx.Exec(ctx, extensionPublishSchema)
		return err
	}
	if version != 1 {
		return upgradeError()
	}
	_, err := tx.Exec(ctx, `SELECT installation,target,modules FROM extension_p1_upgrade_artifacts LIMIT 0`)
	return err
}

func validateMigrationArtifacts(p upgrade.Plan, a migrationArtifacts) error {
	b := a.Target
	m := b.Registration.Manifest
	if b.Installation != p.Installation || !channelIDPattern.MatchString(b.Channel) || len(b.Registration.Module) == 0 || len(b.Registration.Module) > extensions.MaxModule {
		return upgradeError()
	}
	if m.HostAPI != "space.plugin.p1" || m.ABI != "space_plugin_p1" || m.UI != "space.ui.p1" || m.Contribution != "overview" || m.Query != "overview" || m.Command != "acknowledge" {
		return upgradeError()
	}
	hash := mutationPackageHash(b)
	if hex.EncodeToString(hash[:]) != p.Target || len(a.Modules) != len(p.Steps) || len(b.Events) > 32 {
		return upgradeError()
	}
	for _, event := range b.Events {
		if !channelIDPattern.MatchString(event) {
			return upgradeError()
		}
	}
	for _, step := range p.Steps {
		module := a.Modules[step.ID]
		if len(module) == 0 || len(module) > extensions.MaxModule || upgradeHash(module) != step.Digest {
			return upgradeError()
		}
	}
	return nil
}

func readMigrationArtifacts(ctx context.Context, tx pgx.Tx, p upgrade.Plan) (migrationArtifacts, error) {
	var a migrationArtifacts
	var target, modules []byte
	err := tx.QueryRow(ctx, `SELECT target,modules FROM extension_p1_upgrade_artifacts WHERE installation=$1`, p.Installation).Scan(&target, &modules)
	if err != nil {
		return a, err
	}
	if json.Unmarshal(target, &a.Target) != nil || json.Unmarshal(modules, &a.Modules) != nil {
		return a, upgradeError()
	}
	return a, validateMigrationArtifacts(p, a)
}

func checkPublishBackup(ctx context.Context, tx pgx.Tx, tables map[string]bool) error {
	var version int
	if err := tx.QueryRow(ctx, `SELECT COALESCE((to_jsonb(m)->>'publish_version')::int,0) FROM extension_p1_meta m WHERE singleton`).Scan(&version); err != nil {
		return err
	}
	exists := tables["extension_p1_upgrade_artifacts"]
	if version == 0 && !exists {
		return nil
	}
	if version != 1 || !exists || !tables["extension_p1_upgrade_jobs"] || !tables["extension_p1_upgrade_steps"] {
		return upgradeError()
	}
	rows, err := tx.Query(ctx, `SELECT j.plan FROM extension_p1_upgrade_artifacts a LEFT JOIN extension_p1_upgrade_jobs j USING(installation)`)
	if err != nil {
		return err
	}
	plans, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return err
	}
	for _, raw := range plans {
		p, err := upgrade.Decode(raw)
		if err != nil {
			return err
		}
		if _, err = readMigrationArtifacts(ctx, tx, p); err != nil {
			return err
		}
	}
	return nil
}

// Опубликованная регистрация хранится вместе с пакетом в БД, а не в памяти
// прежнего процесса. Неизвестная исходная конфигурация не заменяется молча.
func (s *Store) publishedBindings(ctx context.Context, bindings map[string]ExtensionP1MutationBinding) (map[string]ExtensionP1MutationBinding, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass('extension_p1_upgrade_artifacts') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return bindings, nil
	}
	result := make(map[string]ExtensionP1MutationBinding, len(bindings))
	for instance, binding := range bindings {
		result[instance] = binding
	}
	rows, err := s.pool.Query(ctx, `SELECT j.instance,j.plan,a.target FROM extension_p1_upgrade_jobs j JOIN extension_p1_upgrade_artifacts a USING(installation) WHERE j.published`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var instance string
		var raw, target []byte
		if err = rows.Scan(&instance, &raw, &target); err != nil {
			return nil, err
		}
		p, err := upgrade.Decode(raw)
		if err != nil {
			return nil, err
		}
		b, ok := result[instance]
		if !ok {
			return nil, upgradeError()
		}
		hash := mutationPackageHash(b)
		digest := hex.EncodeToString(hash[:])
		if b.Installation != p.Installation || (digest != p.Source && digest != p.Target) {
			return nil, upgradeError()
		}
		var next ExtensionP1MutationBinding
		if json.Unmarshal(target, &next) != nil || next.Installation != b.Installation || next.Channel != b.Channel {
			return nil, upgradeError()
		}
		hash = mutationPackageHash(next)
		if hex.EncodeToString(hash[:]) != p.Target {
			return nil, upgradeError()
		}
		result[instance] = next
	}
	return result, rows.Err()
}
