package postgres

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
)

func registryPageSize(size int32) (int, error) {
	if size < 0 || size > 100 {
		return 0, mutationError(codes.InvalidArgument, "registry_page_size")
	}
	if size == 0 {
		return 50, nil
	}
	return int(size), nil
}

// Авторизация и чтение удерживают те же блокировки полномочий, что и COMMIT команды.
func (s *ExtensionP1ManagedService) registryRead(ctx context.Context, read func(context.Context, pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ctx, err := authn.AuthorizeCall(ctx, s.control.store, "/space.v1.ExtensionP1Service/RegistryRead")
	if err != nil {
		return err
	}
	tx, err := s.control.store.pool.Begin(ctx)
	if err != nil {
		return mutationDatabaseError(err)
	}
	defer tx.Rollback(ctx)
	if _, err = authorizeLifecycleTx(ctx, tx, nil); err != nil {
		return mutationDatabaseError(err)
	}
	var stale bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM extension_p1_installations WHERE installation=ANY($1::text[]) AND generation<>$2)`, s.installationPage("", len(s.control.installations)), s.generation).Scan(&stale); err != nil {
		return mutationDatabaseError(err)
	}
	if stale {
		return mutationError(codes.FailedPrecondition, "lifecycle_fenced")
	}
	if err = read(ctx, tx); err != nil {
		return mutationDatabaseError(err)
	}
	if err = currentExtensionSession(ctx, tx); err != nil {
		return err
	}
	return mutationDatabaseError(tx.Commit(ctx))
}

func (s *ExtensionP1ManagedService) readInstallation(ctx context.Context, tx pgx.Tx, id string) (*pb.ExtensionInstallation, error) {
	item := s.control.installations[id]
	if item == nil {
		return nil, forbidden()
	}
	if err := checkLifecycleChannels(ctx, tx, item.channels); err != nil {
		return nil, err
	}
	result := &pb.ExtensionInstallation{InstallationId: id}
	err := tx.QueryRow(ctx, `SELECT state,revision,generation,binding_hash FROM extension_p1_installations WHERE installation=$1 FOR SHARE`, id).Scan(&result.State, &result.Revision, &result.Generation, &result.BindingSha256)
	if err != nil {
		return nil, err
	}
	if result.Generation != s.generation {
		return nil, mutationError(codes.FailedPrecondition, "lifecycle_fenced")
	}
	if item.failed.Load() {
		return nil, mutationError(codes.Unavailable, "lifecycle_failed")
	}
	return result, nil
}

func (s *ExtensionP1ManagedService) GetExtensionInstallation(ctx context.Context, r *pb.GetExtensionInstallationRequest) (*pb.GetExtensionInstallationResponse, error) {
	if r == nil || !channelIDPattern.MatchString(r.InstallationId) {
		return nil, mutationError(codes.InvalidArgument, "registry_request")
	}
	result := new(pb.GetExtensionInstallationResponse)
	err := s.registryRead(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result.Installation, err = s.readInstallation(ctx, tx, r.InstallationId)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *ExtensionP1ManagedService) ListExtensionInstallations(ctx context.Context, r *pb.ListExtensionInstallationsRequest) (*pb.ListExtensionInstallationsResponse, error) {
	if r == nil || (r.AfterInstallationId != "" && !channelIDPattern.MatchString(r.AfterInstallationId)) {
		return nil, mutationError(codes.InvalidArgument, "registry_request")
	}
	size, err := registryPageSize(r.PageSize)
	if err != nil {
		return nil, err
	}
	result := &pb.ListExtensionInstallationsResponse{Generation: s.generation}
	err = s.registryRead(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if (r.Generation != "" && r.Generation != s.generation) || (r.AfterInstallationId != "" && r.Generation == "") {
			return mutationError(codes.FailedPrecondition, "lifecycle_generation")
		}
		names := s.installationPage(r.AfterInstallationId, size+1)
		for _, name := range names {
			item, err := s.readInstallation(ctx, tx, name)
			if err != nil {
				return err
			}
			result.Installations = append(result.Installations, item)
		}
		if len(result.Installations) > size {
			result.Installations = result.Installations[:size]
			result.NextAfterInstallationId = result.Installations[size-1].InstallationId
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *ExtensionP1ManagedService) installationPage(after string, limit int) []string {
	names := make([]string, 0, len(s.control.installations))
	for name := range s.control.installations {
		if name > after {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > limit {
		names = names[:limit]
	}
	return names
}

func (s *ExtensionP1ManagedService) ChangeExtensionInstallation(ctx context.Context, r *pb.ChangeExtensionInstallationRequest) (*pb.ChangeExtensionInstallationResponse, error) {
	if r == nil || !channelIDPattern.MatchString(r.InstallationId) || len(r.Generation) != 32 || r.ExpectedRevision < 1 || r.ExpectedRevision > 2147483646 {
		return nil, mutationError(codes.InvalidArgument, "registry_request")
	}
	if r.Action != "enable" && r.Action != "disable" && r.Action != "remove" {
		return nil, mutationError(codes.InvalidArgument, "registry_action")
	}
	d, err := s.Change(ctx, authn.Token(ctx), r.Generation, r.InstallationId, r.Action, r.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	return &pb.ChangeExtensionInstallationResponse{State: d.State, Revision: d.Revision, Generation: s.generation}, nil
}
