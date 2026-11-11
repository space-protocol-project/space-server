package extensionp1

import (
	"context"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) ListExtensionContributions(ctx context.Context, r *pb.ListExtensionContributionsRequest) (*pb.ListExtensionContributionsResponse, error) {
	if authn.Token(ctx) == "" {
		return nil, status.Error(codes.Unauthenticated, "session_required")
	}
	if r == nil || r.InstanceId == "" || len(r.InstanceId) > 64 || r.Kind < 0 || r.Kind > pb.ExtensionContributionKind_EXTENSION_CONTRIBUTION_KIND_SECTION {
		return nil, status.Error(codes.InvalidArgument, "invalid_request")
	}
	items, digest, err := s.dispatcher.Contributions(ctx, authn.Token(ctx), r.InstanceId)
	if err != nil {
		return nil, rpcError(err)
	}
	result := &pb.ListExtensionContributionsResponse{RegistrySha256: digest[:]}
	for _, item := range items {
		kind := pb.ExtensionContributionKind_EXTENSION_CONTRIBUTION_KIND_OVERVIEW
		if item.Kind == "section" {
			kind = pb.ExtensionContributionKind_EXTENSION_CONTRIBUTION_KIND_SECTION
		}
		if r.Kind != pb.ExtensionContributionKind_EXTENSION_CONTRIBUTION_KIND_UNSPECIFIED && r.Kind != kind {
			continue
		}
		result.Contributions = append(result.Contributions, &pb.ExtensionContribution{Id: item.ID, Kind: kind, Title: item.Title, Query: item.Query})
	}
	return result, nil
}
