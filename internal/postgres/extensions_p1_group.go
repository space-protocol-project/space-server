package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
)

// PrepareGroupUpgrade фиксирует весь состав установки и все исходные снимки
// одной транзакцией. Внешний сетевой адаптер в P1 не предоставляется.
func (s *ExtensionP1ManagedService) PrepareGroupUpgrade(ctx context.Context, token string, members []ExtensionP1GroupMember, evidence map[string]ExtensionP1GroupEvidence) error {
	raw, err := json.Marshal(members)
	if err != nil || len(raw) > 40000000 {
		return upgradeError()
	}
	var owned []ExtensionP1GroupMember
	if json.Unmarshal(raw, &owned) != nil {
		return upgradeError()
	}
	g := groupRecord{Format: 1}
	for _, m := range owned {
		g.Members = append(g.Members, versionRecord{Format: 1, Instance: m.Instance, Plan: m.Plan, Artifacts: migrationArtifacts{Target: m.Target, Modules: m.Modules}, Steps: []versionStep{}})
	}
	if err = validateGroupMembers(g); err != nil {
		return err
	}
	if len(evidence) != len(g.Members) {
		return upgrade.ErrEvidence
	}
	name := g.Members[0].Plan.Installation
	return s.upgradeTransaction(ctx, token, name, func(ctx context.Context, tx pgx.Tx, principal string) error {
		if err := checkGroupStorage(ctx, tx); err != nil {
			return err
		}
		if err := groupMembership(ctx, tx, g); err != nil {
			return err
		}
		if err := blockUpgradeChange(ctx, tx, name); err != nil {
			return err
		}
		heads, err := readGroupHeads(ctx, tx)
		if err != nil {
			return err
		}
		if err = checkPendingGroupLink(heads[name], g); err != nil {
			return err
		}
		for i := range g.Members {
			r := &g.Members[i]
			if err = s.attestGroupMember(ctx, tx, g, *r, evidence[r.Instance], false); err != nil {
				return err
			}
			r.PreparedBy = principal
			r.Seed, err = readMigrationData(ctx, tx, name, r.Instance, r.Plan.SchemaFrom)
			if err != nil {
				return err
			}
			r.Document = r.Seed
		}
		if err = g.validate(false); err != nil {
			return err
		}
		raw, err := json.Marshal(g)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO extension_p1_group_jobs(installation,digest,record,record_hash) VALUES($1,$2,$3,$4)
 ON CONFLICT(installation) DO UPDATE SET digest=EXCLUDED.digest,record=EXCLUDED.record,record_hash=EXCLUDED.record_hash,published=false`, name, g.digest(), raw, upgradeHash(raw))
		return err
	})
}

func (s *ExtensionP1ManagedService) attestGroupMember(ctx context.Context, tx pgx.Tx, g groupRecord, r versionRecord, e ExtensionP1GroupEvidence, resume bool) error {
	if e.Digest != g.digest() {
		return upgrade.ErrEvidence
	}
	b, err := groupBinding(ctx, tx, r, false)
	if err != nil {
		return err
	}
	if resume {
		b.Generation = r.Plan.Generation
	}
	observation, err := upgrade.Begin(r.Plan, b, e.Verification, e.Checkpoint, true)
	if err != nil {
		return err
	}
	if !resume {
		d := s.control.installations[r.Plan.Installation].coordinator.Preview(observation)
		if d.Code != "OK" {
			return lifecycleDecisionError(d)
		}
	}
	return nil
}

func (s *ExtensionP1ManagedService) ReadGroupUpgrade(ctx context.Context, token, installation string) (ExtensionP1GroupProgress, error) {
	var result ExtensionP1GroupProgress
	err := s.upgradeTransaction(ctx, token, installation, func(ctx context.Context, tx pgx.Tx, _ string) error {
		g, published, err := readGroup(ctx, tx, installation)
		if err != nil {
			return err
		}
		result = g.progress(published)
		return nil
	})
	return result, err
}

// AdvanceGroupUpgrade исполняет один шаг одного экземпляра. Digest и курсор
// обязательны: повтор после потери COMMIT сначала читает постоянный журнал.
func (s *ExtensionP1ManagedService) AdvanceGroupUpgrade(ctx context.Context, token, installation, digest, instance string, expectedStep int, host *extensions.ProcessHost, e ExtensionP1GroupEvidence) (ExtensionP1GroupProgress, error) {
	if host == nil || host.Status().State != "ready" {
		return ExtensionP1GroupProgress{}, extensions.ErrIsolation
	}
	var result ExtensionP1GroupProgress
	err := s.upgradeTransaction(ctx, token, installation, func(ctx context.Context, tx pgx.Tx, principal string) error {
		g, published, err := readGroup(ctx, tx, installation)
		if err != nil {
			return err
		}
		if published || digest != g.digest() {
			return upgradeError()
		}
		if err = groupMembership(ctx, tx, g); err != nil {
			return err
		}
		index := -1
		for i, r := range g.Members {
			if r.Instance == instance {
				index = i
			}
		}
		if index < 0 {
			return upgradeError()
		}
		r := &g.Members[index]
		if len(r.Steps) != expectedStep || expectedStep >= len(r.Plan.Steps) {
			return upgradeError()
		}
		if err = s.attestGroupMember(ctx, tx, g, *r, e, true); err != nil {
			return err
		}
		if err = checkGroupSource(ctx, tx, *r, false); err != nil {
			return err
		}
		step := r.Plan.Steps[expectedStep]
		output, err := host.Invoke(ctx, extensions.Invocation{Module: r.Artifacts.Modules[step.ID], Input: r.Document})
		if err != nil {
			return err
		}
		data, err := decodeMigrationData(output, step.To)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		r.Steps = append(r.Steps, versionStep{Digest: step.Digest, Input: upgradeHash(r.Document), Generation: s.generation, Principal: principal, Output: raw, WASM: true})
		r.Document = raw
		if err = saveGroup(ctx, tx, g, false); err != nil {
			return err
		}
		result = g.progress(false)
		return nil
	})
	return result, err
}
