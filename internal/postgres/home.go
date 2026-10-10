package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

// SealHomeSetup выполняется до открытия внешнего входа нового домашнего сервера.
// При обычных перезапусках выбранная владельцем политика не изменяется.
func (s *Store) SealHomeSetup(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var owner string
	var revision int64
	err = tx.QueryRow(ctx, "UPDATE space_settings SET registration_policy='closed',revision=revision+1 WHERE singleton=true AND owner_id IS NOT NULL AND registration_policy<>'closed' RETURNING owner_id,revision").Scan(&owner, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO admin_audit(principal_id,action,revision) VALUES($1,'home.registration.closed',$2)", owner, revision); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
