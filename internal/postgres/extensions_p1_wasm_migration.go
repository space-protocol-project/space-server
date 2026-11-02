package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
)

// PrepareWASMUpgrade берёт исходные данные из БД под той же блокировкой, что
// план и артефакты. JSON гостя не может выбрать другую установку или таблицу.
func (s *ExtensionP1ManagedService) PrepareWASMUpgrade(ctx context.Context, token, instance string, p upgrade.Plan, v upgrade.Verification, c upgrade.Checkpoint, target ExtensionP1MutationBinding, modules map[string][]byte) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	p, err = upgrade.Decode(raw)
	if err != nil {
		return err
	}
	targetRaw, err := json.Marshal(target)
	if err != nil {
		return err
	}
	modulesRaw, err := json.Marshal(modules)
	if err != nil {
		return err
	}
	var a migrationArtifacts
	if json.Unmarshal(targetRaw, &a.Target) != nil || json.Unmarshal(modulesRaw, &a.Modules) != nil {
		return upgradeError()
	}
	if err = validateMigrationArtifacts(p, a); err != nil {
		return err
	}
	// Первый профиль публикации: исходная схема P1 равна 1. Последующие
	// обновления потребуют архивации завершённого журнала, а не его удаления.
	if p.SchemaFrom != 1 {
		return upgradeError()
	}
	return s.upgradeTransaction(ctx, token, p.Installation, func(ctx context.Context, tx pgx.Tx, principal string) error {
		b, err := upgradeBinding(ctx, tx, p, instance)
		if err != nil {
			return err
		}
		o, err := upgrade.Begin(p, b, v, c, true)
		if err != nil {
			return err
		}
		d := s.control.installations[p.Installation].coordinator.Preview(o)
		if d.Code != "OK" {
			return lifecycleDecisionError(d)
		}
		var channel string
		if err = tx.QueryRow(ctx, `SELECT channel FROM extension_p1_instances WHERE installation=$1 AND instance=$2`, p.Installation, instance).Scan(&channel); err != nil {
			return err
		}
		if channel != a.Target.Channel {
			return upgradeError()
		}
		seed, err := readMigrationData(ctx, tx, p.Installation, instance, p.SchemaFrom)
		if err != nil {
			return err
		}
		digest, _ := p.Digest()
		if _, err = tx.Exec(ctx, `INSERT INTO extension_p1_upgrade_jobs(installation,instance,digest,plan,seed,document,next_step,principal) VALUES($1,$2,$3,$4,$5,$5,0,$6)`, p.Installation, instance, digest, raw, seed, principal); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO extension_p1_upgrade_artifacts(installation,target,modules) VALUES($1,$2,$3)`, p.Installation, targetRaw, modulesRaw)
		return err
	})
}

// ResumeWASMUpgrade принимает именно изолированный ProcessHost, а не
// произвольный Runtime. Компиляция и исполнение не попадают в процесс ядра.
func (s *ExtensionP1ManagedService) ResumeWASMUpgrade(ctx context.Context, token, installation string, host *extensions.ProcessHost, attest ExtensionP1UpgradeAttest) (ExtensionP1UpgradeProgress, error) {
	if host == nil || host.Status().State != "ready" {
		return ExtensionP1UpgradeProgress{}, extensions.ErrIsolation
	}
	var p ExtensionP1UpgradeProgress
	var a migrationArtifacts
	err := s.upgradeTransaction(ctx, token, installation, func(ctx context.Context, tx pgx.Tx, _ string) error {
		var err error
		p, _, err = readUpgrade(ctx, tx, installation)
		if err != nil {
			return err
		}
		a, err = readMigrationArtifacts(ctx, tx, p.Plan)
		return err
	})
	if err != nil {
		return ExtensionP1UpgradeProgress{}, err
	}
	for !p.Ready() {
		if attest == nil {
			return ExtensionP1UpgradeProgress{}, upgrade.ErrEvidence
		}
		plan := p.Plan
		plan.Steps = append([]upgrade.Step(nil), p.Plan.Steps...)
		v, c, err := attest(ctx, plan)
		if err != nil {
			return ExtensionP1UpgradeProgress{}, err
		}
		p, err = s.advanceUpgrade(ctx, token, installation, p.Digest, p.NextStep, v, c, func(ctx context.Context, step upgrade.Step, input []byte) ([]byte, error) {
			if _, err := decodeMigrationData(input, step.From); err != nil {
				return nil, err
			}
			module := a.Modules[step.ID]
			if upgradeHash(module) != step.Digest {
				return nil, upgrade.ErrEvidence
			}
			output, err := host.Invoke(ctx, extensions.Invocation{Module: module, Input: input})
			if err != nil {
				return nil, err
			}
			data, err := decodeMigrationData(output, step.To)
			if err != nil {
				return nil, err
			}
			return json.Marshal(data)
		}, true)
		if err != nil {
			return ExtensionP1UpgradeProgress{}, err
		}
	}
	return p, nil
}
