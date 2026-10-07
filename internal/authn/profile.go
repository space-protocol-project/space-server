package authn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Transcript имеет только ASCII-строки фиксированного профиля и целые Unix seconds.
// Порядок полей лексикографический; это ограниченное подмножество JCS, не общий JCS parser.
type Transcript struct {
	AuthEpoch       int64    `json:"auth_epoch"`
	ChallengeID     string   `json:"challenge_id"`
	DevicePublicKey string   `json:"device_public_key"`
	ExpiresAt       int64    `json:"expires_at"`
	GrantExpiresAt  int64    `json:"grant_expires_at"`
	GrantID         string   `json:"grant_id"`
	IssuedAt        int64    `json:"issued_at"`
	Nonce           string   `json:"nonce"`
	Origin          string   `json:"origin"`
	PrincipalID     string   `json:"principal_id"`
	Purpose         string   `json:"purpose"`
	RootPublicKey   string   `json:"root_public_key"`
	Scopes          []string `json:"scopes"`
	ServerID        string   `json:"server_id"`
	Version         int      `json:"v"`
}

func (t Transcript) Canonical() ([]byte, error) {
	stringsToCheck := []string{t.ChallengeID, t.DevicePublicKey, t.GrantID, t.Nonce, t.Origin, t.PrincipalID, t.Purpose, t.RootPublicKey, t.ServerID}
	stringsToCheck = append(stringsToCheck, t.Scopes...)
	for _, value := range stringsToCheck {
		for _, char := range value {
			if char < 32 || char > 126 || char == '<' || char == '>' || char == '&' {
				return nil, fmt.Errorf("Transcript вне поддержанного ASCII-профиля")
			}
		}
	}
	for _, value := range []int64{t.AuthEpoch, t.ExpiresAt, t.GrantExpiresAt, t.IssuedAt} {
		if value < 0 || value > 9007199254740991 {
			return nil, fmt.Errorf("Число вне точного JSON-профиля")
		}
	}
	return json.Marshal(t)
}

func (t Transcript) SigningBytes() ([]byte, error) {
	prefixes := map[string]string{"device.register": "space/device-register/v1", "auth.login": "space/auth-login/v1", "device.revoke": "space/device-revoke/v1"}
	prefix, ok := prefixes[t.Purpose]
	if !ok {
		return nil, fmt.Errorf("Неизвестное назначение подписи")
	}
	canonical, err := t.Canonical()
	if err != nil {
		return nil, err
	}
	return append([]byte(prefix+"\x00"), canonical...), nil
}

func PrincipalID(public []byte) string {
	hash := sha256.Sum256(public)
	return "u_" + hex.EncodeToString(hash[:])
}

type actorKey struct{}
type tokenKey struct{}

func Actor(ctx context.Context) string {
	id, _ := ctx.Value(actorKey{}).(string)
	if id == "" {
		return "legacy-demo"
	}
	return id
}
func Token(ctx context.Context) string { token, _ := ctx.Value(tokenKey{}).(string); return token }

type Verifier interface {
	Authenticate(context.Context, string) (string, error)
}

func Interceptor(verifier Verifier) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		switch info.FullMethod {
		case "/space.v1.ChannelService/GetManifest", "/space.v1.AuthService/CreateChallenge", "/space.v1.AuthService/CompleteChallenge":
			return handler(ctx, req)
		}
		ctx, err := authorize(ctx, verifier)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func authorize(ctx context.Context, verifier Verifier) (context.Context, error) {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return nil, status.Error(codes.Unauthenticated, "Нужен Bearer access token")
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	id, err := verifier.Authenticate(ctx, token)
	if err != nil {
		return nil, err
	}
	return context.WithValue(context.WithValue(ctx, actorKey{}, id), tokenKey{}, token), nil
}

type authorizedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authorizedStream) Context() context.Context { return s.ctx }
func StreamInterceptor(verifier Verifier) grpc.StreamServerInterceptor {
	return func(service any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := authorize(stream.Context(), verifier)
		if err != nil {
			return err
		}
		return handler(service, &authorizedStream{ServerStream: stream, ctx: ctx})
	}
}
