package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/space-protocol-project/space-server/experiments/extensions/demo"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (m *extensionMutations) lockCommand(ctx context.Context, b extensionMutationBinding, r *pb.ExecuteExtensionCommandRequest) (*pgxpool.Conn, func(), error) {
	key, _ := json.Marshal([]string{"space.extension.p1", b.installation, r.InstanceId, authn.Actor(ctx), r.Command, r.IdempotencyKey})
	hash := sha256.Sum256(key)
	lock := int64(binary.BigEndian.Uint64(hash[:8]))
	conn, err := m.store.pool.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	release := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanup, "SELECT pg_advisory_unlock($1)", lock); err != nil {
			_ = conn.Hijack().Close(cleanup)
			return
		}
		conn.Release()
	}
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lock); err != nil {
		release()
		return nil, nil, err
	}
	return conn, release, nil
}

// WASM исполняется вне транзакции; запись удерживает только связанные строки.
func beginExtensionMutation(ctx context.Context, conn *pgxpool.Conn) (pgx.Tx, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `SET LOCAL lock_timeout='1s'; SET LOCAL statement_timeout='1500ms'`)
	if err == nil {
		err = lockExtensionAuthority(ctx, tx)
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func currentExtensionSession(ctx context.Context, tx pgx.Tx) error {
	hash := sha256.Sum256([]byte(authn.Token(ctx)))
	var actor string
	query := strings.ReplaceAll(sessionQuery, "now()", "clock_timestamp()") + " AND NOT s.push_only"
	if err := tx.QueryRow(ctx, query, hash[:]).Scan(&actor); err != nil || actor != authn.Actor(ctx) {
		return mutationError(codes.PermissionDenied, "permission_denied")
	}
	return ctx.Err()
}

func loadExtensionInstance(ctx context.Context, tx pgx.Tx, b extensionMutationBinding, instance string) (int64, error) {
	var revision int64
	var channel string
	var hash []byte
	err := tx.QueryRow(ctx, `SELECT revision,channel,package_hash FROM extension_p1_instances WHERE installation=$1 AND instance=$2 FOR UPDATE`, b.installation, instance).Scan(&revision, &channel, &hash)
	if err != nil {
		return 0, err
	}
	if channel != b.channel || !bytes.Equal(hash, b.packageHash[:]) {
		return 0, mutationError(codes.FailedPrecondition, "binding_changed")
	}
	return revision, nil
}

func extensionReceipt(ctx context.Context, tx pgx.Tx, b extensionMutationBinding, r *pb.ExecuteExtensionCommandRequest, digest [32]byte) (*pb.ExecuteExtensionCommandResponse, error) {
	var oldHash, response []byte
	var retired bool
	err := tx.QueryRow(ctx, `SELECT digest,response,retired FROM extension_p1_receipts WHERE installation=$1 AND instance=$2 AND principal=$3 AND command=$4 AND key=$5`, b.installation, r.InstanceId, authn.Actor(ctx), r.Command, r.IdempotencyKey).Scan(&oldHash, &response, &retired)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(oldHash, digest[:]) {
		return nil, mutationError(codes.AlreadyExists, "idempotency_conflict")
	}
	if retired {
		return nil, mutationError(codes.FailedPrecondition, "receipt_retired")
	}
	result := new(pb.ExecuteExtensionCommandResponse)
	if err = proto.Unmarshal(response, result); err != nil {
		return nil, err
	}
	if !validReconciledReceipt(result, r) {
		return nil, mutationError(codes.DataLoss, "invalid_receipt")
	}
	result.Replayed = true
	return result, nil
}

func (m *extensionMutations) inspect(ctx context.Context, conn *pgxpool.Conn, b extensionMutationBinding, r *pb.ExecuteExtensionCommandRequest, digest [32]byte) (demo.Principal, *pb.ExecuteExtensionCommandResponse, error) {
	var p demo.Principal
	tx, err := beginExtensionMutation(ctx, conn)
	if err != nil {
		return p, nil, err
	}
	defer tx.Rollback(ctx)
	p, err = m.store.extensionP1PrincipalTx(ctx, tx, b.channel, r.InstanceId, true)
	if err != nil {
		return p, nil, err
	}
	if err = checkExtensionHistory(ctx, tx, r.HistoryId); err != nil {
		return p, nil, err
	}
	revision, err := loadExtensionInstance(ctx, tx, b, r.InstanceId)
	if err != nil {
		return p, nil, err
	}
	prior, err := extensionReceipt(ctx, tx, b, r, digest)
	if err != nil {
		return p, nil, err
	}
	if prior == nil && uint64(revision) != r.ExpectedStateRevision {
		return p, nil, mutationError(codes.Aborted, "revision_conflict")
	}
	if err = currentExtensionSession(ctx, tx); err != nil {
		return p, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return p, nil, err
	}
	return p, prior, nil
}

func mutationDatabaseError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return mutationError(codes.Canceled, "cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return mutationError(codes.DeadlineExceeded, "deadline")
	}
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) {
		switch postgres.Code {
		case "40001", "40P01", "55P03", "23505":
			return mutationError(codes.Aborted, "transaction_conflict")
		case "57014":
			return mutationError(codes.DeadlineExceeded, "deadline")
		}
	}
	code := status.Code(err)
	if code != codes.Unknown {
		return mutationError(code, strings.ToLower(code.String()))
	}
	return mutationError(codes.Internal, "mutation_failed")
}
