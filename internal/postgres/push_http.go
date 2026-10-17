package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"github.com/space-protocol-project/space-server/internal/push"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// WithPush использует тот же TLS listener; capability не принимается другими RPC.
func (s *Store) WithPush(next http.Handler) http.Handler {
	if s == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/push/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		select {
		case s.pushSlots <- struct{}{}:
			defer func() { <-s.pushSlots }()
		default:
			w.WriteHeader(429)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		incoming := metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", r.Header.Get("Authorization")))
		var verifier authn.Verifier = s
		if r.URL.Path != "/api/push/register" {
			verifier = pushVerifier{s}
		}
		ctx, err := authn.AuthorizeCall(incoming, verifier, "/private/push")
		if err != nil {
			w.WriteHeader(pushErrorStatus(err))
			return
		}
		r = r.WithContext(ctx)
		if r.URL.Path == "/api/push/poll" {
			digest := sha256.Sum256([]byte(authn.Token(ctx)))
			if _, active := s.pushActive.LoadOrStore(digest, true); active {
				w.WriteHeader(429)
				return
			}
			defer s.pushActive.Delete(digest)
		}
		if r.URL.Path == "/api/push/register" {
			s.registerPush(w, r)
			return
		}
		if r.URL.Path == "/api/push/poll" {
			s.pollPush(w, r)
			return
		}
		w.WriteHeader(404)
	})
}

type pushVerifier struct{ store *Store }

func (p pushVerifier) Authenticate(ctx context.Context, token string) (string, error) {
	if len(token) != 46 {
		return "", denied()
	}
	hash := sha256.Sum256([]byte(token))
	var principal string
	err := p.store.pool.QueryRow(ctx, sessionQuery+" AND s.push_only", hash[:]).Scan(&principal)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", denied()
	}
	if err != nil {
		return "", databaseError(ctx, err)
	}
	return principal, nil
}

func pushErrorStatus(err error) int {
	if errors.Is(err, pgx.ErrNoRows) || status.Code(err) == codes.Unauthenticated || status.Code(err) == codes.PermissionDenied {
		return 401
	}
	return 503
}

func readPushBody(w http.ResponseWriter, r *http.Request, value any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

func (s *Store) registerPush(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Binding string `json:"binding"`
		Key     string `json:"key"`
	}
	if readPushBody(w, r, &req) != nil || len(req.Binding) != 32 {
		w.WriteHeader(400)
		return
	}
	if _, err := hex.DecodeString(req.Binding); err != nil {
		w.WriteHeader(400)
		return
	}
	key, err := base64.RawURLEncoding.DecodeString(req.Key)
	if err != nil || len(key) != 32 {
		w.WriteHeader(400)
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		w.WriteHeader(503)
		return
	}
	defer tx.Rollback(r.Context())
	if s.lockSession(r.Context(), tx) != nil {
		w.WriteHeader(401)
		return
	}
	hash := sha256.Sum256([]byte(authn.Token(r.Context())))
	var grant string
	if tx.QueryRow(r.Context(), "SELECT grant_id FROM auth_sessions WHERE token_hash=$1 AND NOT push_only", hash[:]).Scan(&grant) != nil {
		w.WriteHeader(401)
		return
	}
	// Замена отзывает предыдущую capability; чужая binding не перезаписывается.
	var owner string
	err = tx.QueryRow(r.Context(), "SELECT grant_id FROM push_registrations WHERE binding=$1", req.Binding).Scan(&owner)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		w.WriteHeader(503)
		return
	}
	if owner != "" && owner != grant {
		w.WriteHeader(409)
		return
	}
	token, err := randomString("st_")
	if err != nil {
		w.WriteHeader(503)
		return
	}
	digest := sha256.Sum256([]byte(token))
	_, err = tx.Exec(r.Context(), "INSERT INTO auth_sessions(token_hash,grant_id,expires_at,push_only) VALUES($1,$2,now()+interval '30 days',true)", digest[:], grant)
	if err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO push_registrations(binding,grant_id,token_hash,secret) VALUES($1,$2,$3,$4) ON CONFLICT(grant_id) DO UPDATE SET binding=EXCLUDED.binding,token_hash=EXCLUDED.token_hash,secret=EXCLUDED.secret", req.Binding, grant, digest[:], key)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), "DELETE FROM auth_sessions WHERE push_only AND grant_id=$1 AND token_hash<>$2", grant, digest[:])
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		w.WriteHeader(503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": token})
}

func (s *Store) pollPush(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ack []string `json:"ack"`
	}
	if readPushBody(w, r, &req) != nil || len(req.Ack) > 64 {
		w.WriteHeader(400)
		return
	}
	hash := sha256.Sum256([]byte(authn.Token(r.Context())))
	timer := time.NewTimer(24 * time.Second)
	defer timer.Stop()
	for {
		values, err := s.pushBatch(r.Context(), hash[:], req.Ack)
		if err != nil {
			w.WriteHeader(pushErrorStatus(err))
			return
		}
		req.Ack = nil
		if len(values) > 0 {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(values)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("[]"))
			return
		case <-time.After(3 * time.Second):
		}
	}
}

type queuedPush struct {
	channel, event string
	sequence       int64
	expires        time.Time
}

func (s *Store) pushBatch(ctx context.Context, hash []byte, ack []string) ([]string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = s.lockSession(ctx, tx); err != nil {
		return nil, err
	}
	var binding, grant string
	var key []byte
	if err = tx.QueryRow(ctx, "SELECT binding,grant_id,secret FROM push_registrations WHERE token_hash=$1 FOR UPDATE", hash).Scan(&binding, &grant, &key); err != nil {
		return nil, err
	}
	if ack == nil {
		ack = []string{}
	}
	rows, err := tx.Query(ctx, "SELECT channel_id,sequence,event,expires_at FROM push_queue WHERE binding=$1 AND NOT(event=ANY($2::text[])) AND expires_at>now() ORDER BY channel_id LIMIT 64", binding, ack)
	if err != nil {
		return nil, err
	}
	var pending []queuedPush
	for rows.Next() {
		var item queuedPush
		if err = rows.Scan(&item.channel, &item.sequence, &item.event, &item.expires); err != nil {
			rows.Close()
			return nil, err
		}
		pending = append(pending, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []string{}
	for _, item := range pending {
		value, allowed, err := pushItem(ctx, tx, binding, grant, key, item)
		if err != nil {
			return nil, err
		}
		if allowed {
			result = append(result, value)
		} else {
			ack = append(ack, item.event)
		}
	}
	// Сначала блокировки grant/channel, затем очередь: тот же порядок, что у записи сообщения.
	if _, err = tx.Exec(ctx, "DELETE FROM push_queue WHERE binding=$1 AND (event=ANY($2::text[]) OR expires_at<=now())", binding, ack); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}

func pushItem(ctx context.Context, tx pgx.Tx, binding, grant string, key []byte, item queuedPush) (string, bool, error) {
	channel, err := channelAccess(ctx, tx, item.channel, "read", true)
	if err != nil {
		if status.Code(err) == codes.NotFound || status.Code(err) == codes.PermissionDenied || status.Code(err) == codes.FailedPrecondition {
			return "", false, nil
		}
		return "", false, err
	}
	if channel.Archived {
		return "", false, nil
	}
	if len(channel.MlsGroupId) > 0 {
		scope, scopeErr := sessionMlsScope(ctx, tx)
		if scopeErr != nil {
			return "", false, scopeErr
		}
		grant = scope.GrantID
		var raw []byte
		if err = tx.QueryRow(ctx, "SELECT envelope FROM mls_envelopes WHERE channel_id=$1 AND sequence=$2", item.channel, item.sequence).Scan(&raw); err != nil {
			return "", false, err
		}
		envelope := new(pb.MlsEnvelope)
		if proto.Unmarshal(raw, envelope) != nil {
			return "", false, errors.New("invalid MLS envelope")
		}
		if envelope.Kind != "application" || envelope.AuthorId == authn.Actor(ctx) {
			return "", false, nil
		}
		var member bool
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM mls_members WHERE channel_id=$1 AND grant_id=$2 AND left_sequence IS NULL AND joined_sequence<=$3)", item.channel, grant, item.sequence).Scan(&member)
		if err != nil {
			return "", false, err
		}
		if !member {
			return "", false, nil
		}
	} else {
		var ownOrDeleted bool
		err = tx.QueryRow(ctx, "SELECT deleted OR author_id=$3 FROM contents WHERE channel_id=$1 AND sequence=$2", item.channel, item.sequence, authn.Actor(ctx)).Scan(&ownOrDeleted)
		if err != nil {
			return "", false, err
		}
		if ownOrDeleted {
			return "", false, nil
		}
	}
	target := push.Target{Binding: binding, Channel: item.channel, Sequence: item.sequence, Event: item.event, Expires: item.expires.Unix()}
	if len(channel.MlsGroupId) > 0 {
		target.Group = base64.StdEncoding.EncodeToString(channel.MlsGroupId)
	}
	value, err := push.Seal(key, target, time.Now())
	return value, err == nil, err
}
