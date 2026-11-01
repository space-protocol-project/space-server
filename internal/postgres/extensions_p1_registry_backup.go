package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func checkLifecycleBackup(ctx context.Context, tx pgx.Tx, table, restore bool) error {
	var version int
	err := tx.QueryRow(ctx, `SELECT COALESCE((to_jsonb(m)->>'lifecycle_version')::int,0) FROM extension_p1_meta m WHERE singleton`).Scan(&version)
	if err != nil {
		return err
	}
	if version == 0 && !table {
		return nil
	}
	if version != 1 || !table {
		return errors.New("Неполный или неизвестный реестр установок")
	}
	var invalid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM extension_p1_installations r
 WHERE r.state NOT IN ('disabled','enabled','disabling','quarantined','removed')
 OR r.revision NOT BETWEEN 1 AND 2147483646 OR r.generation !~ '^[0-9a-f]{32}$'
 OR octet_length(r.binding_hash)<>32
 OR NOT EXISTS(SELECT 1 FROM extension_p1_instances i WHERE i.installation=r.installation))`).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid {
		return errors.New("Несогласованный реестр установок")
	}
	if restore {
		// Состояние и ревизия сохраняются. Поколение копии не даёт права на вызовы.
		_, err = tx.Exec(ctx, `UPDATE extension_p1_installations SET generation=replace(gen_random_uuid()::text,'-','')`)
	}
	return err
}
