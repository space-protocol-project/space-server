package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/space-protocol-project/space-server/experiments/extensions/demo"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func (m *extensionMutations) commit(ctx context.Context, conn *pgxpool.Conn, b extensionMutationBinding, r *pb.ExecuteExtensionCommandRequest, digest [32]byte, expected demo.Principal, document []byte, plan *pb.ExtensionMutationPlan) (*pb.ExecuteExtensionCommandResponse, error) {
	tx, err := beginExtensionMutation(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	current, err := m.store.extensionP1PrincipalTx(ctx, tx, b.channel, r.InstanceId, true)
	if err != nil {
		return nil, err
	}
	if current != expected {
		return nil, mutationError(codes.PermissionDenied, "access_changed")
	}
	revision, err := loadExtensionInstance(ctx, tx, b, r.InstanceId)
	if err != nil {
		return nil, err
	}
	if uint64(revision) != r.ExpectedStateRevision {
		return nil, mutationError(codes.Aborted, "revision_conflict")
	}
	if err = extensionMutationCapacity(ctx, tx, b, r.InstanceId, len(plan.Events)); err != nil {
		return nil, err
	}
	if err = applyExtensionOperations(ctx, tx, b, r.InstanceId, plan.Operations); err != nil {
		return nil, err
	}
	if err = extensionDataCapacity(ctx, tx, b, r.InstanceId); err != nil {
		return nil, err
	}
	result := &pb.ExecuteExtensionCommandResponse{DocumentJson: document, Effect: pb.ExtensionCommandEffect_EXTENSION_COMMAND_EFFECT_MUTATION, StateRevision: uint64(revision + 1)}
	if err = saveExtensionMutation(ctx, tx, b, r, digest, result, plan.Events); err != nil {
		return nil, err
	}
	if err = currentExtensionSession(ctx, tx); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func extensionMutationCapacity(ctx context.Context, tx pgx.Tx, b extensionMutationBinding, instance string, events int) error {
	var receipts, outbox int64
	err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM extension_p1_receipts WHERE installation=$1 AND instance=$2),(SELECT count(*) FROM extension_p1_outbox WHERE installation=$1 AND instance=$2)`, b.installation, instance).Scan(&receipts, &outbox)
	if err != nil {
		return err
	}
	if receipts >= 1024 || outbox+int64(events) > 4096 {
		return mutationError(codes.ResourceExhausted, "mutation_storage_quota")
	}
	return nil
}
func extensionDataCapacity(ctx context.Context, tx pgx.Tx, b extensionMutationBinding, instance string) error {
	var keys, size int64
	err := tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(octet_length(key)+octet_length(value)),0) FROM extension_p1_data WHERE installation=$1 AND instance=$2`, b.installation, instance).Scan(&keys, &size)
	if err != nil {
		return err
	}
	if keys > 256 || size > 1024*1024 {
		return mutationError(codes.ResourceExhausted, "data_quota")
	}
	return nil
}

func applyExtensionOperations(ctx context.Context, tx pgx.Tx, b extensionMutationBinding, instance string, operations []*pb.ExtensionMutationOperation) error {
	for _, op := range operations {
		var err error
		switch change := op.Change.(type) {
		case *pb.ExtensionMutationOperation_PutJson:
			_, err = tx.Exec(ctx, `INSERT INTO extension_p1_data VALUES($1,$2,$3,$4) ON CONFLICT(installation,instance,key) DO UPDATE SET value=EXCLUDED.value`, b.installation, instance, op.Key, change.PutJson)
		case *pb.ExtensionMutationOperation_Delete:
			_, err = tx.Exec(ctx, `DELETE FROM extension_p1_data WHERE installation=$1 AND instance=$2 AND key=$3`, b.installation, instance, op.Key)
		default:
			return mutationError(codes.InvalidArgument, "invalid_operation")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func saveExtensionMutation(ctx context.Context, tx pgx.Tx, b extensionMutationBinding, r *pb.ExecuteExtensionCommandRequest, digest [32]byte, result *pb.ExecuteExtensionCommandResponse, events []*pb.ExtensionMutationEvent) error {
	for ordinal, event := range events {
		// После отката к старой копии ревизия может использоваться снова.
		// Новый ID не должен совпасть с событием утраченной ветви истории.
		var id [32]byte
		if _, err := rand.Read(id[:]); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO extension_p1_outbox VALUES($1,$2,$3,$4,$5,$6,$7)`, b.installation, r.InstanceId, int64(result.StateRevision), ordinal, hex.EncodeToString(id[:]), event.Type, event.PayloadJson)
		if err != nil {
			return err
		}
	}
	response, _ := proto.Marshal(result)
	_, err := tx.Exec(ctx, `INSERT INTO extension_p1_receipts(installation,instance,principal,command,key,digest,response) VALUES($1,$2,$3,$4,$5,$6,$7)`, b.installation, r.InstanceId, authn.Actor(ctx), r.Command, r.IdempotencyKey, digest[:], response)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE extension_p1_instances SET revision=$3 WHERE installation=$1 AND instance=$2`, b.installation, r.InstanceId, int64(result.StateRevision))
	return err
}
