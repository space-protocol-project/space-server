package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"time"
)

func (s *ExtensionP1ManagedService) ListExtensionInstallationAudit(ctx context.Context, r *pb.ListExtensionInstallationAuditRequest) (*pb.ListExtensionInstallationAuditResponse, error) {
	if r == nil || !channelIDPattern.MatchString(r.InstallationId) || r.AfterRevision < 0 || r.ThroughRevision < 0 || r.AfterRevision > 2147483646 || r.ThroughRevision > 2147483646 {
		return nil, mutationError(codes.InvalidArgument, "registry_request")
	}
	size, err := registryPageSize(r.PageSize)
	if err != nil {
		return nil, err
	}
	result := &pb.ListExtensionInstallationAuditResponse{Generation: s.generation}
	err = s.registryRead(ctx, func(ctx context.Context, tx pgx.Tx) error {
		item, err := s.readInstallation(ctx, tx, r.InstallationId)
		if err != nil {
			return err
		}
		if r.Generation != item.Generation {
			return mutationError(codes.FailedPrecondition, "lifecycle_generation")
		}
		upper, err := registryAuditUpper(r, item.Revision)
		if err != nil {
			return err
		}
		result.ThroughRevision = upper
		result.Entries, err = readRegistryAudit(ctx, tx, r.InstallationId, r.AfterRevision, upper, size+1)
		if err != nil {
			return err
		}
		if len(result.Entries) > size {
			result.Entries = result.Entries[:size]
			result.NextAfterRevision = result.Entries[size-1].Revision
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func registryAuditUpper(r *pb.ListExtensionInstallationAuditRequest, current int64) (int64, error) {
	upper := r.ThroughRevision
	if upper == 0 {
		if r.AfterRevision != 0 {
			return 0, mutationError(codes.InvalidArgument, "registry_audit_cursor")
		}
		upper = current
	}
	if upper > current || r.AfterRevision > upper {
		return 0, mutationError(codes.InvalidArgument, "registry_audit_cursor")
	}
	return upper, nil
}

func readRegistryAudit(ctx context.Context, tx pgx.Tx, id string, after, upper int64, limit int) ([]*pb.ExtensionInstallationAuditEntry, error) {
	rows, err := tx.Query(ctx, `SELECT revision,generation,action,actor,COALESCE(principal,''),COALESCE(previous_state,''),state,created_at FROM extension_p1_lifecycle_audit WHERE installation=$1 AND revision>$2 AND revision<=$3 ORDER BY revision LIMIT $4`, id, after, upper, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []*pb.ExtensionInstallationAuditEntry
	for rows.Next() {
		entry := new(pb.ExtensionInstallationAuditEntry)
		var created time.Time
		if err = rows.Scan(&entry.Revision, &entry.Generation, &entry.Action, &entry.Actor, &entry.PrincipalId, &entry.PreviousState, &entry.State, &created); err != nil {
			return nil, err
		}
		entry.CreatedAt = created.UTC().Format(time.RFC3339Nano)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}
