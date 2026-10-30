package postgres

import (
	"context"
	"crypto/sha256"

	"github.com/jackc/pgx/v5"
	"github.com/space-protocol-project/space-server/internal/authn"
)

// Порядок совместим с сопряжением и ротацией: pairing → principal → grants → session.
// Ядро создаёт grant и его связь с pairing в одной транзакции; parent_grant_id
// и grant_id сессии после создания неизменяемы. Новая привязка существующего
// grant к pairing потребует пересмотра этой границы (включая защиту от фантомов).
func lockExtensionAuthority(ctx context.Context, tx pgx.Tx) error {
	hash := sha256.Sum256([]byte(authn.Token(ctx)))
	var principal, grant, parent string
	err := tx.QueryRow(ctx, `SELECT g.principal_id,g.id,COALESCE(g.parent_grant_id,'')
 FROM auth_sessions s JOIN device_grants g ON g.id=s.grant_id WHERE s.token_hash=$1`, hash[:]).Scan(&principal, &grant, &parent)
	if err != nil || principal != authn.Actor(ctx) {
		return denied()
	}
	grants := []string{grant, parent}
	if _, err = tx.Exec(ctx, `SELECT id FROM device_pairings WHERE grant_id=ANY($1::text[]) ORDER BY id FOR SHARE`, grants); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM principals WHERE id=$1 FOR SHARE`, principal); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM device_grants WHERE id=ANY($1::text[]) ORDER BY id FOR SHARE`, grants); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT token_hash FROM auth_sessions WHERE token_hash=$1 FOR SHARE`, hash[:]); err != nil {
		return err
	}
	return currentExtensionSession(ctx, tx)
}
