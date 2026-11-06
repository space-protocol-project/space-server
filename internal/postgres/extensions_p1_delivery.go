package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	"time"

	"github.com/space-protocol-project/space-server/experiments/extensions/demo"
	"google.golang.org/grpc/codes"
)

type extensionDeliveryKey struct{}

type extensionDeliverySnapshot struct {
	principal                demo.Principal
	installation, generation string
	revision                 int64
	write                    bool
}

// Снимок берётся из тех же источников авторизации, что и проверка COMMIT.
// Транзакция не удерживается во время WASM и не охватывает сетевую отправку.
func (s *ExtensionP1ManagedService) deliverySnapshot(ctx context.Context, instance string, write bool) (extensionDeliverySnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	p := extensionDeliverySnapshot{installation: s.control.instances[instance], write: write}
	tx, err := s.control.store.pool.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	p, err = s.deliverySnapshotTx(ctx, tx, instance, write)
	if err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}

func (s *ExtensionP1ManagedService) deliverySnapshotTx(ctx context.Context, tx pgx.Tx, instance string, write bool) (extensionDeliverySnapshot, error) {
	p := extensionDeliverySnapshot{installation: s.control.instances[instance], write: write}
	var err error
	p.principal, err = s.control.store.extensionP1PrincipalTx(ctx, tx, s.control.channels[instance], instance, write)
	if err != nil {
		return p, err
	}
	var state string
	err = tx.QueryRow(ctx, `SELECT generation,revision,state FROM extension_p1_installations WHERE installation=$1 FOR SHARE`, p.installation).Scan(&p.generation, &p.revision, &state)
	if err != nil {
		return p, err
	}
	if p.generation != s.control.generation || state != "enabled" {
		return p, mutationError(codes.FailedPrecondition, "lifecycle_fenced")
	}
	return p, nil
}

func (s *ExtensionP1ManagedService) checkDelivery(ctx context.Context, instance string) error {
	before, ok := ctx.Value(extensionDeliveryKey{}).(extensionDeliverySnapshot)
	if !ok || before.principal.Instance != instance {
		return mutationError(codes.PermissionDenied, "delivery_snapshot_required")
	}
	after, err := s.deliverySnapshot(ctx, instance, before.write)
	if err != nil {
		return err
	}
	return compareDeliverySnapshot(before, after)
}

func compareDeliverySnapshot(before, after extensionDeliverySnapshot) error {
	if after.principal != before.principal {
		return mutationError(codes.PermissionDenied, "delivery_authority_changed")
	}
	if after != before {
		return mutationError(codes.FailedPrecondition, "delivery_snapshot_changed")
	}
	return nil
}

func (m *extensionMutations) checkAdmissionCommit(ctx context.Context, tx pgx.Tx, instance string, current demo.Principal) error {
	if m.lifecycle == nil {
		return nil
	}
	before, ok := ctx.Value(extensionDeliveryKey{}).(extensionDeliverySnapshot)
	if !ok || before.principal.Instance != instance || !before.write {
		return mutationError(codes.PermissionDenied, "admission_snapshot_required")
	}
	after := extensionDeliverySnapshot{principal: current, installation: m.lifecycle.instances[instance], write: true}
	var state string
	err := tx.QueryRow(ctx, `SELECT generation,revision,state FROM extension_p1_installations WHERE installation=$1 FOR SHARE`, after.installation).Scan(&after.generation, &after.revision, &state)
	if err != nil {
		return err
	}
	if state != "enabled" {
		return mutationError(codes.FailedPrecondition, "lifecycle_fenced")
	}
	return compareDeliverySnapshot(before, after)
}
