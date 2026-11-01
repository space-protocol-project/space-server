package postgres

import (
	"context"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
)

func (s *ExtensionP1ManagedService) begin(ctx context.Context, instance string, write bool) (context.Context, func(), error) {
	if authn.Token(ctx) == "" {
		return nil, nil, mutationError(codes.Unauthenticated, "session_required")
	}
	installation, ok := s.control.instances[instance]
	if !ok {
		return nil, nil, denied()
	}
	if _, err := s.control.store.extensionP1Principal(ctx, s.control.channels[instance], instance, write); err != nil {
		return nil, nil, err
	}
	i := s.control.installations[installation]
	unlock, err := i.lock(ctx)
	if err != nil {
		return nil, nil, mutationDatabaseError(err)
	}
	defer unlock()
	if i.failed.Load() {
		return nil, nil, mutationError(codes.Unavailable, "lifecycle_failed")
	}
	if err := s.control.checkGeneration(ctx, installation); err != nil {
		return nil, nil, err
	}
	call, release, err := i.coordinator.Begin(ctx)
	if err != nil {
		return nil, nil, mutationError(codes.FailedPrecondition, "extension_not_enabled")
	}
	return call, release, nil
}

func (s *ExtensionP1ManagedService) QueryExtension(ctx context.Context, r *pb.QueryExtensionRequest) (*pb.QueryExtensionResponse, error) {
	if r == nil {
		return nil, mutationError(codes.InvalidArgument, "invalid_request")
	}
	call, release, err := s.begin(ctx, r.InstanceId, false)
	if err != nil {
		return nil, err
	}
	defer release()
	result, err := s.service.QueryExtension(call, r)
	if call.Err() != nil {
		return nil, mutationDatabaseError(call.Err())
	}
	if err == nil {
		err = s.control.checkGeneration(call, s.control.instances[r.InstanceId])
	}
	return result, err
}

func (s *ExtensionP1ManagedService) ExecuteExtensionCommand(ctx context.Context, r *pb.ExecuteExtensionCommandRequest) (*pb.ExecuteExtensionCommandResponse, error) {
	if r == nil {
		return nil, mutationError(codes.InvalidArgument, "invalid_request")
	}
	call, release, err := s.begin(ctx, r.InstanceId, true)
	if err != nil {
		return nil, err
	}
	defer release()
	// COMMIT может предшествовать отмене, поэтому поздний Canceled не означает откат.
	result, err := s.service.ExecuteExtensionCommand(call, r)
	if call.Err() != nil {
		return nil, mutationDatabaseError(call.Err())
	}
	if err == nil {
		err = s.control.checkGeneration(call, s.control.instances[r.InstanceId])
	}
	return result, err
}

func (s *ExtensionP1ManagedService) GetExtensionMutationContext(ctx context.Context, r *pb.GetExtensionMutationContextRequest) (*pb.GetExtensionMutationContextResponse, error) {
	if r == nil {
		return nil, mutationError(codes.InvalidArgument, "invalid_request")
	}
	call, release, err := s.begin(ctx, r.InstanceId, true)
	if err != nil {
		return nil, err
	}
	defer release()
	result, err := s.service.GetExtensionMutationContext(call, r)
	if call.Err() != nil {
		return nil, mutationDatabaseError(call.Err())
	}
	if err == nil {
		err = s.control.checkGeneration(call, s.control.instances[r.InstanceId])
	}
	return result, err
}

// Сверка не запускает код и не создаёт эффект. Она доступна при выключении
// и удалении кода, но по-прежнему требует действующих сессии и ACL ядра.
func (s *ExtensionP1ManagedService) ReconcileExtensionCommand(ctx context.Context, r *pb.ReconcileExtensionCommandRequest) (*pb.ReconcileExtensionCommandResponse, error) {
	return s.service.ReconcileExtensionCommand(ctx, r)
}
