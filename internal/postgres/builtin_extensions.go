package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/space-protocol-project/space-server/internal/authn"
	"github.com/space-protocol-project/space-server/internal/builtin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Store) builtinTransaction(ctx context.Context) (pgx.Tx, error) {
	// В отличие от старых служебных вызовов, профиль P2 не допускает анонимный context.
	if authn.Token(ctx) == "" || authn.Actor(ctx) == "" {
		return nil, denied()
	}
	actor, err := s.Authenticate(ctx, authn.Token(ctx))
	if err != nil {
		return nil, err
	}
	if actor != authn.Actor(ctx) {
		return nil, denied()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = s.lockSession(ctx, tx); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	m, err := membership(ctx, tx, authn.Actor(ctx), true)
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	if m.Blocked {
		tx.Rollback(ctx)
		return nil, forbidden()
	}
	return tx, nil
}

func builtinSection(ctx context.Context, tx pgx.Tx, instance string) (builtin.Section, error) {
	var out builtin.Section
	var channel string
	err := tx.QueryRow(ctx, `SELECT i.id,i.view_id,i.channel_id,p.id,p.plugin_id,p.version
        FROM builtin_extension_instances i JOIN builtin_extension_installations p ON p.id=i.installation_id
        WHERE i.id=$1`, instance).Scan(&out.InstanceID, &out.ViewID, &channel, &out.Installation.ID, &out.Installation.PluginID, &out.Installation.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, status.Error(codes.NotFound, "Раздел не найден")
	}
	if err != nil {
		return out, databaseError(ctx, err)
	}
	out.Channel, err = channelAccess(ctx, tx, channel, "read", true)
	if err != nil {
		return builtin.Section{}, err
	}
	if out.Channel.Archived {
		return builtin.Section{}, status.Error(codes.FailedPrecondition, "Раздел в архиве")
	}
	return out, nil
}

func (s *Store) BuiltinSection(ctx context.Context, instance string) (builtin.Section, error) {
	tx, err := s.builtinTransaction(ctx)
	if err != nil {
		return builtin.Section{}, err
	}
	defer tx.Rollback(ctx)
	if len(instance) == 0 || len(instance) > 100 {
		return builtin.Section{}, status.Error(codes.InvalidArgument, "Некорректный экземпляр")
	}
	return builtinSection(ctx, tx, instance)
}

func (s *Store) BuiltinSections(ctx context.Context) ([]builtin.Section, error) {
	tx, err := s.builtinTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	channels, err := s.channelList(ctx, tx, false, false, true)
	if err != nil {
		return nil, err
	}
	out := make([]builtin.Section, 0, len(channels))
	for _, channel := range channels {
		if !channel.Permissions.Read {
			continue
		}
		instance := "builtin:" + channel.Id + ":" + channel.Views[0].Id
		section, err := builtinSection(ctx, tx, instance)
		if err != nil {
			return nil, err
		}
		out = append(out, section)
	}
	return out, nil
}
