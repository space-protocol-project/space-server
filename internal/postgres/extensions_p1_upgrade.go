package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

//go:embed extensions_p1_upgrade.sql
var extensionUpgradeSchema string

// ExtensionP1UpgradeProgress описывает подготовку, а не публикацию пакета.
type ExtensionP1UpgradeProgress struct {
	Plan     upgrade.Plan
	Digest   string
	NextStep int
	Document []byte
}

func (p ExtensionP1UpgradeProgress) Ready() bool { return p.NextStep == len(p.Plan.Steps) }

// ExtensionP1MigrationStep — доверенный чистый преобразователь. Никаких внешних
// эффектов: после отката или потери процесса вычисление может повториться.
// WASM, SQL и сетевой доступ гостю через этот интерфейс не предоставляются.
type ExtensionP1MigrationStep func(context.Context, upgrade.Step, []byte) ([]byte, error)

// ExtensionP1UpgradeAttest заново проверяет доверие и копию перед каждым шагом.
// Свидетельства не сохраняются как бессрочное разрешение на исполнение.
type ExtensionP1UpgradeAttest func(context.Context, upgrade.Plan) (upgrade.Verification, upgrade.Checkpoint, error)

// ResumeUpgrade продолжает с зафиксированного курсора. Запускается доверенным
// ядром после открытия новой оболочки; сетевой API этого метода не существует.
func (s *ExtensionP1ManagedService) ResumeUpgrade(ctx context.Context, token, installation string, attest ExtensionP1UpgradeAttest, execute ExtensionP1MigrationStep) (ExtensionP1UpgradeProgress, error) {
	p, err := s.ReadUpgrade(ctx, token, installation)
	if err != nil {
		return ExtensionP1UpgradeProgress{}, err
	}
	for !p.Ready() {
		if attest == nil {
			return ExtensionP1UpgradeProgress{}, upgrade.ErrEvidence
		}
		// Копия плана не позволяет адаптеру менять сохранённые шаги через slice.
		plan := p.Plan
		plan.Steps = append([]upgrade.Step(nil), plan.Steps...)
		v, c, err := attest(ctx, plan)
		if err != nil {
			return ExtensionP1UpgradeProgress{}, err
		}
		p, err = s.AdvanceUpgrade(ctx, token, installation, p.Digest, p.NextStep, v, c, execute)
		if err != nil {
			return ExtensionP1UpgradeProgress{}, err
		}
	}
	return p, nil
}

func upgradeHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func upgradeError() error { return mutationError(codes.FailedPrecondition, "upgrade_state") }

// upgradeTransaction сериализует план с ACL, сменой поколения и lifecycle.
// Потеря подтверждения COMMIT закрывает локальную оболочку до нового открытия.
func (s *ExtensionP1ManagedService) upgradeTransaction(ctx context.Context, token, installation string, run func(context.Context, pgx.Tx, string) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ctx, err := authn.AuthorizeCall(metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)), s.control.store, "/space.p1.upgrade/Manage")
	if err != nil {
		return err
	}
	i := s.control.installations[installation]
	if i == nil {
		return forbidden()
	}
	release, err := i.lock(ctx)
	if err != nil {
		return mutationDatabaseError(err)
	}
	defer release()
	if i.failed.Load() {
		return mutationError(codes.Unavailable, "lifecycle_failed")
	}
	tx, err := s.control.store.pool.Begin(ctx)
	if err != nil {
		return mutationDatabaseError(err)
	}
	defer tx.Rollback(context.Background())
	principal, err := authorizeLifecycleTx(ctx, tx, i.channels)
	if err != nil {
		return mutationDatabaseError(err)
	}
	var generation string
	if err = tx.QueryRow(ctx, `SELECT generation FROM extension_p1_installations WHERE installation=$1 FOR UPDATE`, installation).Scan(&generation); err != nil {
		return mutationDatabaseError(err)
	}
	if generation != s.generation {
		return upgradeError()
	}
	if err = run(ctx, tx, principal); err != nil {
		return mutationDatabaseError(err)
	}
	if err = currentExtensionSession(ctx, tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		i.failed.Store(true)
		return mutationDatabaseError(err)
	}
	return nil
}

// PrepareUpgrade сохраняет неизменяемый план и исходный документ из уже
// проверенной копии. Пока поддерживается ровно один instance на установку.
// Проверка содержимого копии и реальных подписей остаётся у доверенного ядра.
func (s *ExtensionP1ManagedService) PrepareUpgrade(ctx context.Context, token, instance string, p upgrade.Plan, v upgrade.Verification, c upgrade.Checkpoint, seed []byte) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return upgrade.ErrPlan
	}
	p, err = upgrade.Decode(raw)
	if err != nil {
		return err
	}
	seed = bytes.Clone(seed)
	if extensions.CheckJSON(seed) != nil {
		return upgrade.ErrPlan
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
		digest, _ := p.Digest()
		_, err = tx.Exec(ctx, `INSERT INTO extension_p1_upgrade_jobs(installation,instance,digest,plan,seed,document,next_step,principal) VALUES($1,$2,$3,$4,$5,$5,0,$6)`, p.Installation, instance, digest, raw, seed, principal)
		return err
	})
}

func upgradeBinding(ctx context.Context, tx pgx.Tx, p upgrade.Plan, instance string) (upgrade.Binding, error) {
	var b upgrade.Binding
	var state string
	var count int
	err := tx.QueryRow(ctx, `SELECT s.server_id,r.installation,r.generation,r.revision,r.state,encode(i.package_hash,'hex'),i.revision,
 (SELECT count(*) FROM extension_p1_instances WHERE installation=r.installation)
 FROM extension_p1_installations r JOIN extension_p1_instances i USING(installation) CROSS JOIN server_state s
 WHERE r.installation=$1 AND i.instance=$2 AND s.singleton`, p.Installation, instance).Scan(&b.Space, &b.Installation, &b.Generation, &b.Revision, &state, &b.Package, &b.DataRevision, &count)
	if err != nil {
		return b, err
	}
	if state != "disabled" || count != 1 {
		return b, upgradeError()
	}
	b.Schema = p.SchemaFrom // У P1 нет рабочего каталога версий схем; значение подтверждает ядро.
	return b, nil
}

func (s *ExtensionP1ManagedService) ReadUpgrade(ctx context.Context, token, installation string) (ExtensionP1UpgradeProgress, error) {
	var p ExtensionP1UpgradeProgress
	err := s.upgradeTransaction(ctx, token, installation, func(ctx context.Context, tx pgx.Tx, _ string) error {
		var err error
		p, _, err = readUpgrade(ctx, tx, installation)
		return err
	})
	if err != nil {
		return ExtensionP1UpgradeProgress{}, err
	}
	return p, nil
}

// AdvanceUpgrade фиксирует результат одного шага и журнал одной транзакцией.
// При неопределённом COMMIT вызывающий открывает новую оболочку, читает NextStep
// и продолжает с него. Исходное поколение в Plan при этом не переписывается.
func (s *ExtensionP1ManagedService) AdvanceUpgrade(ctx context.Context, token, installation, digest string, expectedStep int, v upgrade.Verification, c upgrade.Checkpoint, execute ExtensionP1MigrationStep) (ExtensionP1UpgradeProgress, error) {
	var p ExtensionP1UpgradeProgress
	err := s.upgradeTransaction(ctx, token, installation, func(ctx context.Context, tx pgx.Tx, principal string) error {
		var instance string
		var err error
		p, instance, err = readUpgrade(ctx, tx, installation)
		if err != nil {
			return err
		}
		if p.Digest != digest || p.NextStep != expectedStep {
			return mutationError(codes.Aborted, "upgrade_cursor")
		}
		b, err := upgradeBinding(ctx, tx, p.Plan, instance)
		if err != nil {
			return err
		}
		// Новое поколение уже проверено под SQL-блокировкой. Свидетельства остаются
		// привязаны к исходному плану, а исходные данные обязаны быть неизменны.
		b.Generation = p.Plan.Generation
		if _, err = upgrade.Begin(p.Plan, b, v, c, true); err != nil {
			return err
		}
		if p.Ready() {
			return nil
		}
		if execute == nil {
			return upgradeError()
		}
		step := p.Plan.Steps[p.NextStep]
		output, err := execute(ctx, step, bytes.Clone(p.Document))
		if err != nil {
			return err
		}
		if extensions.CheckJSON(output) != nil {
			return upgrade.ErrEvidence
		}
		output = bytes.Clone(output)
		_, err = tx.Exec(ctx, `INSERT INTO extension_p1_upgrade_steps(installation,ordinal,step_digest,input_digest,output,generation,principal) VALUES($1,$2,$3,$4,$5,$6,$7)`, installation, p.NextStep, step.Digest, upgradeHash(p.Document), output, s.generation, principal)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE extension_p1_upgrade_jobs SET next_step=next_step+1,document=$2 WHERE installation=$1`, installation, output)
		p.NextStep++
		p.Document = output
		return err
	})
	if err != nil {
		return ExtensionP1UpgradeProgress{}, err
	}
	return p, nil
}
