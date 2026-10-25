package postgres

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func profileName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if !utf8.ValidString(name) || len(name) > 160 || utf8.RuneCountInString(name) > 40 || strings.IndexFunc(name, unsafeProfileRune) >= 0 {
		return "", status.Error(codes.InvalidArgument, "Имя должно содержать до 40 символов без управляющих знаков")
	}
	return name, nil
}

func unsafeProfileRune(r rune) bool {
	return unicode.IsControl(r) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

func (s *Store) profileTransaction(ctx context.Context) (pgx.Tx, error) {
	if authn.Token(ctx) == "" {
		return nil, denied()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = s.lockSession(ctx, tx); err == nil {
		var m *pb.Member
		m, err = membership(ctx, tx, authn.Actor(ctx), false)
		if err == nil && m.Blocked {
			err = forbidden()
		}
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func participantProfile(ctx context.Context, tx pgx.Tx, id string, lock bool) (*pb.ParticipantProfile, error) {
	query := "SELECT principal_id,nickname,profile_avatar,profile_revision FROM memberships WHERE principal_id=$1 AND NOT blocked"
	if lock {
		query += " FOR UPDATE"
	}
	out := new(pb.ParticipantProfile)
	err := tx.QueryRow(ctx, query, id).Scan(&out.PrincipalId, &out.DisplayName, &out.AvatarPng, &out.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, forbidden()
	}
	return out, databaseErrorOrNil(ctx, err)
}

func (s *Store) GetParticipantProfile(ctx context.Context, _ *pb.GetParticipantProfileRequest) (*pb.GetParticipantProfileResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := s.profileTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	profile, err := participantProfile(ctx, tx, authn.Actor(ctx), false)
	if err != nil {
		return nil, err
	}
	return &pb.GetParticipantProfileResponse{Profile: profile}, nil
}

func (s *Store) UpdateParticipantProfile(ctx context.Context, req *pb.UpdateParticipantProfileRequest) (*pb.UpdateParticipantProfileResponse, error) {
	name, err := profileName(req.DisplayName)
	if err != nil {
		return nil, err
	}
	if req.ExpectedRevision < 0 || (req.AvatarPng != nil && req.RemoveAvatar) {
		return nil, status.Error(codes.InvalidArgument, "Некорректная правка профиля")
	}
	var avatar []byte
	if req.AvatarPng != nil {
		avatar, err = profileAvatar(req.AvatarPng)
		if err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := s.profileTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Проверка уникальности и запись профиля сериализованы до конца транзакции.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73616015)"); err != nil {
		return nil, databaseError(ctx, err)
	}
	current, err := participantProfile(ctx, tx, authn.Actor(ctx), true)
	if err != nil {
		return nil, err
	}
	if current.Revision != req.ExpectedRevision {
		return nil, status.Error(codes.Aborted, "Профиль изменён на другом устройстве. Обновите его перед сохранением")
	}
	if err = requireUniqueProfileName(ctx, tx, current.PrincipalId, name); err != nil {
		return nil, err
	}
	current.DisplayName = name
	if req.AvatarPng != nil {
		current.AvatarPng = avatar
	}
	if req.RemoveAvatar {
		current.AvatarPng = nil
	}
	err = tx.QueryRow(ctx, `UPDATE memberships SET nickname=$2,profile_avatar=$3,profile_revision=profile_revision+1,revision=revision+1 WHERE principal_id=$1 AND NOT blocked RETURNING profile_revision`, current.PrincipalId, name, current.AvatarPng).Scan(&current.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, forbidden()
	}
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.UpdateParticipantProfileResponse{Profile: current}, nil
}

func (s *Store) ListParticipantProfiles(ctx context.Context, req *pb.ListParticipantProfilesRequest) (*pb.ListParticipantProfilesResponse, error) {
	if len(req.PrincipalIds) == 0 || len(req.PrincipalIds) > 50 {
		return nil, status.Error(codes.InvalidArgument, "Нужно от 1 до 50 ID участников")
	}
	for _, id := range req.PrincipalIds {
		if id == "" || !utf8.ValidString(id) || len(id) > 128 || strings.IndexFunc(id, unicode.IsControl) >= 0 {
			return nil, status.Error(codes.InvalidArgument, "Некорректный ID участника")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := s.profileTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	channel, err := channelAccess(ctx, tx, req.ChannelId, "read", true)
	if err != nil {
		return nil, err
	}
	if channel.Archived {
		return nil, forbidden()
	}
	if err = requireProfileReader(ctx, tx, channel); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, "SELECT m.principal_id,m.nickname,m.profile_avatar,m.profile_revision"+mentionMembers+" AND m.principal_id=ANY($2) ORDER BY m.principal_id", req.ChannelId, req.PrincipalIds)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	result := new(pb.ListParticipantProfilesResponse)
	for rows.Next() {
		profile := new(pb.ParticipantProfile)
		if err = rows.Scan(&profile.PrincipalId, &profile.DisplayName, &profile.AvatarPng, &profile.Revision); err != nil {
			return nil, databaseError(ctx, err)
		}
		result.Profiles = append(result.Profiles, profile)
	}
	return result, databaseErrorOrNil(ctx, rows.Err())
}

func requireProfileReader(ctx context.Context, tx pgx.Tx, channel *pb.Channel) error {
	if len(channel.MlsGroupId) == 0 {
		return nil
	}
	scope, err := sessionMlsScope(ctx, tx)
	if err != nil {
		return err
	}
	_, err = activeMlsMember(ctx, tx, channel.Id, scope.GrantID)
	return err
}
