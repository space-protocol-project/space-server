package postgres

import (
	"context"
	"crypto/sha256"

	"github.com/space-protocol-project/space-server/internal/authn"
	"github.com/space-protocol-project/space-server/internal/voice"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Store) VoiceIdentity(ctx context.Context, channel string) (voice.Identity, error) {
	var identity voice.Identity
	if authn.Token(ctx) == "" {
		return identity, denied()
	}
	principal, err := s.Authenticate(ctx, authn.Token(ctx))
	if err != nil {
		return identity, err
	}
	if principal != authn.Actor(ctx) {
		return identity, denied()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return identity, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if err = s.lockSession(ctx, tx); err != nil {
		return identity, err
	}
	c, err := channelAccess(ctx, tx, channel, "read", true)
	if err != nil {
		return identity, err
	}
	if c.Archived || len(c.MlsGroupId) > 0 || len(c.Views) != 1 || c.Views[0].Type != "voice_room" {
		return identity, status.Error(codes.FailedPrecondition, "Нужна действующая голосовая комната")
	}
	_, err = membership(ctx, tx, principal, false)
	if err != nil {
		return identity, err
	}
	hash := sha256.Sum256([]byte(authn.Token(ctx)))
	identity.Actor.PrincipalID = principal
	if err = tx.QueryRow(ctx, "SELECT s.grant_id,i.server_id,COALESCE(m.nickname,'') FROM auth_sessions s CROSS JOIN server_state i LEFT JOIN memberships m ON m.principal_id=$2 WHERE s.token_hash=$1", hash[:], principal).Scan(&identity.Actor.DeviceID, &identity.Actor.ServerID, &identity.Name); err != nil {
		return identity, databaseError(ctx, err)
	}
	identity.CanPublish = c.Permissions.Write
	if err = tx.Commit(ctx); err != nil {
		return identity, databaseError(ctx, err)
	}
	return identity, nil
}
