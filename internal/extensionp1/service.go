// Package extensionp1 предоставляет отдельно включаемый экспериментальный RPC.
package extensionp1

import (
	"context"
	"encoding/json"
	"errors"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/demo"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Service struct {
	pb.UnimplementedExtensionP1ServiceServer
	dispatcher *demo.Dispatcher
	mutations  MutationExecutor
}

// MutationExecutor выбирается доверенным ядром, не полем effect клиента.
type MutationExecutor interface {
	Handles(instance, command string) bool
	Execute(context.Context, *pb.ExecuteExtensionCommandRequest, func(context.Context) ([]byte, error)) (*pb.ExecuteExtensionCommandResponse, error)
}

func NewWithMutations(host extensions.Runtime, authorize demo.Authorize, registrations map[string]demo.Registration, executor MutationExecutor) (*Service, error) {
	s, err := New(host, authorize, registrations)
	if err != nil {
		return nil, err
	}
	s.mutations = executor
	return s, nil
}

func New(host extensions.Runtime, authorize demo.Authorize, registrations map[string]demo.Registration) (*Service, error) {
	dispatcher, err := demo.New(host, authorize, registrations)
	if err != nil {
		return nil, err
	}
	return &Service{dispatcher: dispatcher}, nil
}

func (s *Service) QueryExtension(ctx context.Context, r *pb.QueryExtensionRequest) (*pb.QueryExtensionResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid_request")
	}
	output, err := s.invoke(ctx, r.InstanceId, "query", r.Query, r.PayloadJson)
	if err != nil {
		return nil, err
	}
	return &pb.QueryExtensionResponse{DocumentJson: output, UiApi: "space.ui.p1"}, nil
}

func (s *Service) ExecuteExtensionCommand(ctx context.Context, r *pb.ExecuteExtensionCommandRequest) (*pb.ExecuteExtensionCommandResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid_request")
	}
	if s.mutations != nil && s.mutations.Handles(r.InstanceId, r.Command) {
		return s.mutations.Execute(ctx, r, func(ctx context.Context) ([]byte, error) {
			return s.invoke(ctx, r.InstanceId, "command", r.Command, r.PayloadJson)
		})
	}
	// Прежний конструктор сохраняет только stateless acknowledge.
	// Без явно подключённого исполнителя мутации запрещены.
	if r.Effect != pb.ExtensionCommandEffect_EXTENSION_COMMAND_EFFECT_NONE || r.IdempotencyKey != "" || r.ExpectedStateRevision != 0 {
		return nil, status.Error(codes.FailedPrecondition, "mutation_not_supported")
	}
	output, err := s.invoke(ctx, r.InstanceId, "command", r.Command, r.PayloadJson)
	if err != nil {
		return nil, err
	}
	return &pb.ExecuteExtensionCommandResponse{DocumentJson: output, Effect: r.Effect}, nil
}

func (s *Service) invoke(ctx context.Context, instance, kind, name string, payload []byte) ([]byte, error) {
	if authn.Token(ctx) == "" {
		return nil, status.Error(codes.Unauthenticated, "session_required")
	}
	if len(payload) > extensions.MaxJSON || extensions.CheckJSON(payload) != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid_request")
	}
	// Decode сохраняет точные поля, запрет дубликатов и предел всего envelope.
	raw, err := json.Marshal(map[string]any{"protocol": demo.Protocol, "instance": instance, "kind": kind, "name": name, "payload": json.RawMessage(payload)})
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid_request")
	}
	request, err := demo.Decode(raw)
	if err != nil {
		return nil, rpcError(err)
	}
	output, err := s.dispatcher.Dispatch(ctx, authn.Token(ctx), request)
	return output, rpcError(err)
}

func rpcError(err error) error {
	if err == nil {
		return nil
	}
	code, message := codes.Internal, "extension_failed"
	switch {
	case errors.Is(err, demo.ErrDenied):
		code, message = codes.PermissionDenied, "permission_denied"
	case errors.Is(err, demo.ErrRequest):
		code, message = codes.InvalidArgument, "invalid_request"
	case errors.Is(err, demo.ErrVersion):
		code, message = codes.FailedPrecondition, "unsupported_version"
	case errors.Is(err, extensions.ErrQuota):
		code, message = codes.ResourceExhausted, "quota_exceeded"
	case errors.Is(err, extensions.ErrIsolation):
		code, message = codes.Unavailable, "unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		code, message = codes.DeadlineExceeded, "deadline"
	case errors.Is(err, context.Canceled):
		code, message = codes.Canceled, "cancelled"
	}
	return status.Error(code, message)
}
