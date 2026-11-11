package postgres

import (
	"context"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
)

// Метаданные проходят те же барьеры жизненного цикла и доставки, что и query.
func (s *ExtensionP1ManagedService) ListExtensionContributions(ctx context.Context, r *pb.ListExtensionContributionsRequest) (*pb.ListExtensionContributionsResponse, error) {
	if r == nil {
		return nil, mutationError(codes.InvalidArgument, "invalid_request")
	}
	call, release, err := s.begin(ctx, r.InstanceId, false)
	if err != nil {
		return nil, err
	}
	defer release()
	result, err := s.service.ListExtensionContributions(call, r)
	if call.Err() != nil {
		return nil, mutationDatabaseError(call.Err())
	}
	if err == nil {
		err = s.checkDelivery(call, r.InstanceId)
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}
