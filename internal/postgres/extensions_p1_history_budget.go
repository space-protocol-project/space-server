package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
)

const (
	historyInstallationRecords int64 = 32
	historyInstallationBytes   int64 = 128 << 20
	historyTotalRecords        int64 = 128
	historyTotalBytes          int64 = 512 << 20
)

type historyUsage struct {
	Records, Bytes, TotalRecords, TotalBytes int64
}

func (u historyUsage) admit(size int64) error {
	if size < 0 || size > historyInstallationBytes || size > historyTotalBytes {
		return mutationError(codes.ResourceExhausted, "history_quota")
	}
	if u.Records >= historyInstallationRecords || u.TotalRecords >= historyTotalRecords || u.Bytes > historyInstallationBytes-size || u.TotalBytes > historyTotalBytes-size {
		return mutationError(codes.ResourceExhausted, "history_quota")
	}
	return nil
}

// Вызывается до вставки истории под общей блокировкой extension_p1_meta.
// Считаются точные байты record; это не квота файлов PostgreSQL или WAL.
func admitHistory(ctx context.Context, tx pgx.Tx, installation string, size int) error {
	u, err := readHistoryUsage(ctx, tx, installation)
	if err != nil {
		return err
	}
	return u.admit(int64(size))
}

func readHistoryUsage(ctx context.Context, tx pgx.Tx, installation string) (historyUsage, error) {
	var u historyUsage
	version, err := groupStorageVersion(ctx, tx)
	if err != nil {
		return u, err
	}
	query := `SELECT installation,octet_length(record)::bigint AS size FROM extension_p1_versions`
	if version == 1 {
		query += ` UNION ALL SELECT installation,octet_length(record)::bigint FROM extension_p1_group_versions`
	}
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE installation=$1),COALESCE(sum(size) FILTER(WHERE installation=$1),0),count(*),COALESCE(sum(size),0) FROM (`+query+`) history`, installation).Scan(&u.Records, &u.Bytes, &u.TotalRecords, &u.TotalBytes)
	return u, err
}
