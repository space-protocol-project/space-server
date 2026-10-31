package postgres

import (
	"context"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
)

var extensionHistoryPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (m *extensionMutations) MutationContext(ctx context.Context, instance string) (*pb.GetExtensionMutationContextResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	b, ok := m.bindings[instance]
	if !ok {
		return nil, mutationError(codes.PermissionDenied, "permission_denied")
	}
	select {
	case m.slots <- struct{}{}:
		defer func() { <-m.slots }()
	default:
		return nil, mutationError(codes.ResourceExhausted, "mutation_slots")
	}
	tx, err := m.store.pool.Begin(ctx)
	if err != nil {
		return nil, mutationDatabaseError(err)
	}
	defer tx.Rollback(ctx)
	if err = lockExtensionAuthority(ctx, tx); err != nil {
		return nil, mutationDatabaseError(err)
	}
	if _, err = m.store.extensionP1PrincipalTx(ctx, tx, b.channel, instance, true); err != nil {
		return nil, mutationDatabaseError(err)
	}
	if _, err = loadExtensionInstance(ctx, tx, b, instance); err != nil {
		return nil, mutationDatabaseError(err)
	}
	history, err := loadExtensionHistory(ctx, tx)
	if err != nil {
		return nil, mutationDatabaseError(err)
	}
	if err = currentExtensionSession(ctx, tx); err != nil {
		return nil, mutationDatabaseError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, mutationDatabaseError(err)
	}
	return &pb.GetExtensionMutationContextResponse{HistoryId: history}, nil
}

func loadExtensionHistory(ctx context.Context, tx pgx.Tx) (string, error) {
	var history string
	var version int
	err := tx.QueryRow(ctx, `SELECT version,history_id FROM extension_p1_meta WHERE singleton=true FOR SHARE`).Scan(&version, &history)
	if err != nil {
		return "", err
	}
	if version != 3 || !extensionHistoryPattern.MatchString(history) {
		return "", mutationError(codes.FailedPrecondition, "storage_version")
	}
	return history, nil
}

func checkExtensionHistory(ctx context.Context, tx pgx.Tx, expected string) error {
	history, err := loadExtensionHistory(ctx, tx)
	if err != nil {
		return err
	}
	if history != expected {
		return mutationError(codes.FailedPrecondition, "history_changed")
	}
	return nil
}
