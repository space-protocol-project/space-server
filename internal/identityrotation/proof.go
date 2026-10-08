// Package identityrotation задаёт экспериментальный proof ротации.
// Его использует экспериментальный серверный API; клиентский UI пока отсутствует.
package identityrotation

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// Порядок полей лексикографический; строки ограничены ASCII.
// Добавление полей требует новой версии контракта.
type Transcript struct {
	AuthEpoch          int64    `json:"auth_epoch"`
	ChallengeID        string   `json:"challenge_id"`
	ExpiresAt          int64    `json:"expires_at"`
	IssuedAt           int64    `json:"issued_at"`
	NewDevicePublicKey string   `json:"new_device_public_key"`
	NewRootPublicKey   string   `json:"new_root_public_key"`
	Nonce              string   `json:"nonce"`
	OldRootPublicKey   string   `json:"old_root_public_key"`
	OperationID        string   `json:"operation_id"`
	Origin             string   `json:"origin"`
	PrincipalID        string   `json:"principal_id"`
	Purpose            string   `json:"purpose"`
	Scopes             []string `json:"scopes"`
	ServerID           string   `json:"server_id"`
	V                  int      `json:"v"`
}

type Expected struct {
	PrincipalID        string
	RootPublicKey      ed25519.PublicKey
	AuthEpoch          int64
	ServerID           string
	Origin             string
	ChallengeID        string
	OperationID        string
	NewRootPublicKey   ed25519.PublicKey
	NewDevicePublicKey ed25519.PublicKey
	Scopes             []string
}

var (
	principalPattern = regexp.MustCompile(`^u_[0-9a-f]{64}$`)
	serverPattern    = regexp.MustCompile(`^srv_[0-9a-f]{32}$`)
	challengePattern = regexp.MustCompile(`^rc_[A-Za-z0-9_-]{43}$`)
	operationPattern = regexp.MustCompile(`^ro_[A-Za-z0-9_-]{43}$`)
	noncePattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	originPattern    = regexp.MustCompile(`^http://127\.0\.0\.1:[1-9][0-9]{0,4}$`)
	ErrProof         = errors.New("Недействительное доказательство ротации root")
)

func encoded(key []byte) string { return base64.RawURLEncoding.EncodeToString(key) }
func key(value string) ([]byte, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return decoded, err == nil && len(decoded) == ed25519.PublicKeySize && encoded(decoded) == value
}

func (t Transcript) Validate(expected Expected, now time.Time) error {
	origin, err := url.Parse(t.Origin)
	if err != nil {
		return ErrProof
	}
	port, err := strconv.Atoi(origin.Port())
	if err != nil || port < 1 || port > 65535 {
		return ErrProof
	}
	oldRoot, oldOK := key(t.OldRootPublicKey)
	newRoot, newOK := key(t.NewRootPublicKey)
	_, deviceOK := key(t.NewDevicePublicKey)
	validScopes := slices.Equal(t.Scopes, []string{"chat.read", "chat.write"}) || slices.Equal(t.Scopes, []string{"chat.read", "chat.write", "space.manage"})
	if !oldOK || !newOK || !deviceOK || bytes.Equal(oldRoot, newRoot) ||
		t.V != 1 || t.Purpose != "identity.root.rotate" ||
		t.AuthEpoch < 1 || t.AuthEpoch >= 9007199254740991 || t.AuthEpoch != expected.AuthEpoch ||
		!principalPattern.MatchString(t.PrincipalID) || t.PrincipalID != expected.PrincipalID ||
		!serverPattern.MatchString(t.ServerID) || t.ServerID != expected.ServerID ||
		!originPattern.MatchString(t.Origin) || t.Origin != expected.Origin ||
		!challengePattern.MatchString(t.ChallengeID) || t.ChallengeID != expected.ChallengeID ||
		!operationPattern.MatchString(t.OperationID) || t.OperationID != expected.OperationID ||
		!noncePattern.MatchString(t.Nonce) || !validScopes || !slices.Equal(t.Scopes, expected.Scopes) ||
		t.IssuedAt < 1 || t.IssuedAt > now.Unix()+5 || t.ExpiresAt >= 9007199254740991 || t.ExpiresAt <= now.Unix() ||
		t.ExpiresAt <= t.IssuedAt || t.ExpiresAt-t.IssuedAt > 120 ||
		len(expected.RootPublicKey) != 32 || len(expected.NewRootPublicKey) != 32 || len(expected.NewDevicePublicKey) != 32 ||
		t.OldRootPublicKey != encoded(expected.RootPublicKey) ||
		t.NewRootPublicKey != encoded(expected.NewRootPublicKey) ||
		t.NewDevicePublicKey != encoded(expected.NewDevicePublicKey) {
		return ErrProof
	}
	return nil
}

// SigningBytes возвращает отдельно разделённые доменом подписи старого и нового root.
func (t Transcript) SigningBytes(newRoot bool) ([]byte, error) {
	canonical, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	prefix := "space/root.rotate/old/v1\x00"
	if newRoot {
		prefix = "space/root.rotate/new/v1\x00"
	}
	return append([]byte(prefix), canonical...), nil
}

func (t Transcript) Verify(expected Expected, now time.Time, oldSignature, newSignature []byte) error {
	if err := t.Validate(expected, now); err != nil {
		return err
	}
	oldBytes, err := t.SigningBytes(false)
	if err != nil {
		return err
	}
	newBytes, err := t.SigningBytes(true)
	if err != nil {
		return err
	}
	if !ed25519.Verify(expected.RootPublicKey, oldBytes, oldSignature) || !ed25519.Verify(expected.NewRootPublicKey, newBytes, newSignature) {
		return ErrProof
	}
	return nil
}
