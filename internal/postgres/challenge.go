package postgres

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func validatePairingPurpose(request *pb.CreateChallengeRequest) error {
	if request.PairingId == "" {
		return nil
	}
	if request.Recovery || (request.Purpose != "device.register" && request.Purpose != "device.delegate") {
		return status.Error(codes.InvalidArgument, "Неверный purpose сопряжения")
	}
	return nil
}

func validateChallengeRequest(request *pb.CreateChallengeRequest) error {
	if request.FullManagement && (request.Purpose != "device.register" || request.PairingId == "" || !request.Administrative || request.Recovery) {
		return status.Error(codes.InvalidArgument, "Полный режим выдаётся root только через сопряжение")
	}
	if request.RecoveryGrantId != "" || (request.Recovery && request.Purpose != "device.register") {
		return status.Error(codes.InvalidArgument, "Неверное использование recovery fields")
	}
	if request.Administrative && request.Purpose != "device.register" {
		return status.Error(codes.InvalidArgument, "Administrative разрешён только при регистрации устройства")
	}
	switch request.Purpose {
	case "device.register":
		return validateRegistrationKeys(request)
	case "auth.login", "device.revoke":
		return validateGrantRequest(request)
	default:
		return status.Error(codes.InvalidArgument, "Неизвестный purpose")
	}
}

func validateRegistrationKeys(request *pb.CreateChallengeRequest) error {
	if len(request.RootPublicKey) != ed25519.PublicKeySize || len(request.DevicePublicKey) != ed25519.PublicKeySize || request.GrantId != "" {
		return status.Error(codes.InvalidArgument, "Регистрация требует два публичных ключа Ed25519")
	}
	return nil
}

func validateGrantRequest(request *pb.CreateChallengeRequest) error {
	if request.GrantId == "" || len(request.GrantId) > 128 || len(request.DevicePublicKey) != 0 {
		return status.Error(codes.InvalidArgument, "Нужен grant_id без device_public_key")
	}
	if request.Purpose == "auth.login" && len(request.RootPublicKey) != 0 {
		return status.Error(codes.InvalidArgument, "Вход использует зарегистрированное устройство")
	}
	if request.Purpose == "device.revoke" && len(request.RootPublicKey) != ed25519.PublicKeySize {
		return status.Error(codes.InvalidArgument, "Отзыв требует root_public_key")
	}
	return nil
}

type challengeKeys struct{ root, device []byte }

func (s *AuthService) prepareRegistrationChallenge(ctx context.Context, transaction pgx.Tx, request *pb.CreateChallengeRequest, transcript *authn.Transcript, now time.Time) error {
	var err error
	if request.Administrative {
		transcript.Scopes = append(transcript.Scopes, "space.manage")
	}
	if request.FullManagement { transcript.Scopes = append(transcript.Scopes, "device.manage") }
	transcript.PrincipalID, transcript.AuthEpoch, err = principalForRoot(ctx, transaction, request.RootPublicKey)
	if err != nil {
		return err
	}
	if request.PairingId != "" {
		if err = s.validatePairChallenge(ctx, transaction, request, transcript.PrincipalID); err != nil {
			return err
		}
		transcript.PairingID = request.PairingId
	}
	err = transaction.QueryRow(ctx, "SELECT auth_epoch FROM principals WHERE id=$1", transcript.PrincipalID).Scan(&transcript.AuthEpoch)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return databaseError(ctx, err)
	}
	transcript.GrantID, err = randomString("dg_")
	if err != nil {
		return err
	}
	transcript.GrantExpiresAt = now.Add(30 * 24 * time.Hour).Unix()
	if request.Recovery {
		transcript.Scopes = append(transcript.Scopes, "identity.recover")
		transcript.GrantExpiresAt = now.Add(recoveryLifetime).Unix()
	}

	return nil
}

func (s *AuthService) prepareGrantChallenge(ctx context.Context, transaction pgx.Tx, request *pb.CreateChallengeRequest, transcript *authn.Transcript) (challengeKeys, error) {
	var root, device []byte
	var err error
	var grantExpiry time.Time
	err = transaction.QueryRow(ctx, `SELECT p.id,p.root_public_key,g.device_public_key,p.auth_epoch,g.expires_at,g.scopes FROM device_grants g JOIN principals p ON p.id=g.principal_id
   WHERE g.id=$1 AND g.revoked_at IS NULL AND g.expires_at>now() AND g.auth_epoch=p.auth_epoch`+activeParentCondition+activePairCondition, request.GrantId).Scan(&transcript.PrincipalID, &root, &device, &transcript.AuthEpoch, &grantExpiry, &transcript.Scopes)
	if errors.Is(err, pgx.ErrNoRows) {
		return challengeKeys{}, denied()
	}
	if err != nil {
		return challengeKeys{}, databaseError(ctx, err)
	}
	if request.Purpose == "device.revoke" && !bytes.Equal(request.RootPublicKey, root) {
		return challengeKeys{}, denied()
	}
	transcript.GrantID = request.GrantId
	transcript.GrantExpiresAt = grantExpiry.Unix()
	return challengeKeys{root: root, device: device}, nil
}

func saveChallenge(ctx context.Context, transaction pgx.Tx, transcript *authn.Transcript, keys challengeKeys) ([]byte, error) {
	var err error
	transcript.ChallengeID, err = randomString("ac_")
	if err != nil {
		return nil, err
	}
	transcript.Nonce, err = randomString("")
	if err != nil {
		return nil, err
	}
	canonical, err := transcript.Canonical()
	if err != nil {
		return nil, err
	}
	key := keys.root
	if transcript.Purpose == "auth.login" {
		key = keys.device
	}
	_, err = transaction.Exec(ctx, "INSERT INTO auth_challenges(id,purpose,transcript,verification_key,expires_at) VALUES($1,$2,$3,$4,$5)", transcript.ChallengeID, transcript.Purpose, canonical, key, time.Unix(transcript.ExpiresAt, 0))
	if err != nil {
		return nil, databaseError(ctx, err)
	}

	return canonical, nil
}
