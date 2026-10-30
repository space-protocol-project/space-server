package postgres

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func mutationDigest(b extensionMutationBinding, r *pb.ExecuteExtensionCommandRequest) [32]byte {
	encoded, _ := proto.MarshalOptions{Deterministic: true}.Marshal(r)
	return sha256.Sum256(append(append([]byte("space_plugin_p1\x00"), b.packageHash[:]...), encoded...))
}

// Reconcile не вызывает WASM и не создаёт квитанцию при её отсутствии.
// Блокировки сериализуют проверку с исполнением и отзывом полномочий.
func (m *extensionMutations) Reconcile(ctx context.Context, r *pb.ExecuteExtensionCommandRequest) (*pb.ReconcileExtensionCommandResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if authn.Token(ctx) == "" {
		return nil, mutationError(codes.Unauthenticated, "session_required")
	}
	if !validMutationRequest(r) {
		return nil, mutationError(codes.InvalidArgument, "invalid_mutation_request")
	}
	b, ok := m.bindings[r.InstanceId]
	if !ok || b.command != r.Command {
		return nil, mutationError(codes.PermissionDenied, "permission_denied")
	}
	select {
	case m.slots <- struct{}{}:
		defer func() { <-m.slots }()
	default:
		return nil, mutationError(codes.ResourceExhausted, "mutation_slots")
	}
	conn, release, err := m.lockCommand(ctx, b, r)
	if err != nil {
		return nil, mutationDatabaseError(err)
	}
	defer release()
	result, err := m.reconcileReceipt(ctx, conn, b, r)
	return result, mutationDatabaseError(err)
}

func (m *extensionMutations) reconcileReceipt(ctx context.Context, conn *pgxpool.Conn, b extensionMutationBinding, r *pb.ExecuteExtensionCommandRequest) (*pb.ReconcileExtensionCommandResponse, error) {
	tx, err := beginExtensionMutation(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = m.store.extensionP1PrincipalTx(ctx, tx, b.channel, r.InstanceId, true); err != nil {
		return nil, err
	}
	if _, err = loadExtensionInstance(ctx, tx, b, r.InstanceId); err != nil {
		return nil, err
	}
	receipt, err := extensionReceipt(ctx, tx, b, r, mutationDigest(b, r))
	if err != nil {
		return nil, err
	}
	if receipt != nil && !validReconciledReceipt(receipt, r) {
		return nil, mutationError(codes.DataLoss, "invalid_receipt")
	}
	if err = currentExtensionSession(ctx, tx); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	result := &pb.ReconcileExtensionCommandResponse{Outcome: pb.ExtensionCommandOutcome_EXTENSION_COMMAND_OUTCOME_UNKNOWN}
	if receipt != nil {
		result.Outcome = pb.ExtensionCommandOutcome_EXTENSION_COMMAND_OUTCOME_CONFIRMED
		result.Receipt = receipt
	}
	// Без сохранённого окна и tombstone не объявляем возможность повтора.
	return result, nil
}

func validReconciledReceipt(receipt *pb.ExecuteExtensionCommandResponse, r *pb.ExecuteExtensionCommandRequest) bool {
	return receipt.Effect == pb.ExtensionCommandEffect_EXTENSION_COMMAND_EFFECT_MUTATION &&
		receipt.StateRevision == r.ExpectedStateRevision+1 &&
		len(receipt.DocumentJson) <= extensions.MaxJSON && extensions.CheckJSON(receipt.DocumentJson) == nil
}
