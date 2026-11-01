package postgres

import (
	"context"
	_ "embed"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/space-protocol-project/space-server/experiments/extensions/lifecycle"
	"google.golang.org/grpc/codes"
)

//go:embed extensions_p1_lifecycle_audit.sql
var extensionLifecycleAuditSchema string

func prepareLifecycleAudit(ctx context.Context, tx pgx.Tx, version int) error {
	if version < 2 {
		_, err := tx.Exec(ctx, extensionLifecycleAuditSchema)
		return err
	}
	return checkLifecycleAudit(ctx, tx)
}

func checkpointLifecycleInstallation(ctx context.Context, tx pgx.Tx, installation string) error {
	_, err := tx.Exec(ctx, `INSERT INTO extension_p1_lifecycle_audit(installation,revision,generation,action,actor,state)
 SELECT installation,revision,generation,'checkpoint','checkpoint',state FROM extension_p1_installations WHERE installation=$1`, installation)
	return err
}

// Principal — сохранённый идентификатор автора, не каскадная ссылка на аккаунт.
// Доверенное ядро записывает system без подстановки пользователя или токена.
func (c *extensionLifecycle) recordTransition(ctx context.Context, tx pgx.Tx, installation, action, actor, principal string, revision int64, d lifecycle.Decision) error {
	tag, err := tx.Exec(ctx, `INSERT INTO extension_p1_lifecycle_audit(installation,revision,generation,action,actor,principal,previous_state,state)
 SELECT installation,$2,$3,$4,$5,NULLIF($6,''),state,$7 FROM extension_p1_lifecycle_audit
 WHERE installation=$1 AND revision=$8`, installation, d.Revision, c.generation, action, actor, principal, d.State, revision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return mutationError(codes.FailedPrecondition, "lifecycle_audit_missing")
	}
	return nil
}

// Проверка цепочки выполняется при открытии и проверке копии. Поколение
// последнего события может отличаться от текущего после смены оболочки.
func checkLifecycleAudit(ctx context.Context, tx pgx.Tx) error {
	var invalid bool
	err := tx.QueryRow(ctx, `WITH ordered AS (
 SELECT *,row_number() OVER w AS n,lag(revision) OVER w AS prior_revision,lag(state) OVER w AS prior_state
 FROM extension_p1_lifecycle_audit WINDOW w AS (PARTITION BY installation ORDER BY revision)
 ) SELECT EXISTS(SELECT 1 FROM ordered a LEFT JOIN extension_p1_installations r USING(installation)
 WHERE r.installation IS NULL OR a.revision>r.revision
 OR (a.n=1 AND (a.action<>'checkpoint' OR a.actor<>'checkpoint' OR a.previous_state IS NOT NULL OR a.principal IS NOT NULL))
 OR (a.n>1 AND (a.action='checkpoint' OR a.revision<>a.prior_revision+1 OR a.previous_state IS DISTINCT FROM a.prior_state)))
 OR EXISTS(SELECT 1 FROM extension_p1_installations r LEFT JOIN extension_p1_lifecycle_audit a USING(installation,revision)
 WHERE a.installation IS NULL OR a.state<>r.state)`).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid {
		return errors.New("Журнал жизненного цикла не согласован с реестром")
	}
	return nil
}
