package postgres

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/protobuf/proto"
)

// CheckExtensionP1Backup проверяет полный набор таблиц в одной транзакции.
// Вызывается только обслуживанием остановленного пространства, не RPC.
func CheckExtensionP1Backup(ctx context.Context, address string, restore bool) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, address)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tables, err := extensionBackupTables(ctx, tx)
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return tx.Commit(ctx)
	}
	// Отсутствие любого обязательного компонента запрещает восстановление.
	for _, name := range []string{"meta", "instances", "data", "receipts", "outbox"} {
		if !tables["extension_p1_"+name] {
			return errors.New("Неполная копия хранилища расширений")
		}
	}
	ordered := make([]string, 0, len(tables))
	for name := range tables {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		if _, err = tx.Exec(ctx, "LOCK TABLE "+pgx.Identifier{name}.Sanitize()+" IN SHARE MODE"); err != nil {
			return err
		}
	}
	valid, err := validExtensionReceiptVersion(ctx, tx)
	if err != nil {
		return err
	}
	if !valid {
		return errors.New("Неизвестная версия хранилища расширений")
	}
	if err = checkExtensionBackupRows(ctx, tx, tables["extension_p1_delivery"]); err != nil {
		return err
	}
	if restore && tables["extension_p1_delivery"] {
		// Только в новой восстановленной БД. Курсор и счётчик попыток сохраняются.
		_, err = tx.Exec(ctx, `UPDATE extension_p1_delivery SET lease_revision=0,lease_ordinal=-1,lease_token='',lease_until=NULL,last_ack_token=''`)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func extensionBackupTables(ctx context.Context, tx pgx.Tx) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND left(c.relname,13)='extension_p1_' AND c.relkind IN ('r','p')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tables := map[string]bool{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		switch name {
		case "extension_p1_meta", "extension_p1_instances", "extension_p1_data", "extension_p1_receipts", "extension_p1_outbox", "extension_p1_delivery":
			tables[name] = true
		default:
			return nil, errors.New("Неизвестная таблица расширений в копии")
		}
	}
	return tables, rows.Err()
}

func checkExtensionBackupRows(ctx context.Context, tx pgx.Tx, delivery bool) error {
	var invalid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM extension_p1_outbox o LEFT JOIN extension_p1_instances i USING(installation,instance)
 WHERE i.instance IS NULL OR o.revision<1 OR o.revision>i.revision OR o.ordinal<0)
 OR EXISTS(SELECT 1 FROM extension_p1_data d LEFT JOIN extension_p1_instances i USING(installation,instance) WHERE i.instance IS NULL)
 OR EXISTS(SELECT 1 FROM extension_p1_instances i LEFT JOIN channels c ON c.id=i.channel WHERE c.id IS NULL
 OR i.revision<>(SELECT count(*) FROM extension_p1_receipts r WHERE r.installation=i.installation AND r.instance=i.instance))
 OR EXISTS(SELECT 1 FROM extension_p1_receipts r LEFT JOIN principals p ON p.id=r.principal WHERE p.id IS NULL)`).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid {
		return errors.New("Несогласованные данные расширений в копии")
	}
	if delivery {
		if err = checkExtensionBackupCursor(ctx, tx); err != nil {
			return err
		}
	}
	rows, err := tx.Query(ctx, `SELECT r.installation,r.instance,r.response,i.revision,COALESCE((to_jsonb(r)->>'retired')::boolean,false) FROM extension_p1_receipts r LEFT JOIN extension_p1_instances i USING(installation,instance)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type receiptRevision struct {
		installation, instance string
		revision               uint64
	}
	seen := map[receiptRevision]bool{}
	for rows.Next() {
		var raw []byte
		var revision *int64
		var key receiptRevision
		var retired bool
		if err = rows.Scan(&key.installation, &key.instance, &raw, &revision, &retired); err != nil {
			return err
		}
		response := new(pb.ExecuteExtensionCommandResponse)
		if proto.Unmarshal(raw, response) != nil || revision == nil || *revision <= 0 || response.StateRevision == 0 || response.StateRevision > uint64(*revision) || response.Replayed || response.Effect != pb.ExtensionCommandEffect_EXTENSION_COMMAND_EFFECT_MUTATION {
			return errors.New("Несогласованная квитанция расширения в копии")
		}
		if !validExtensionReceiptBody(response, retired) {
			return errors.New("Тело квитанции не соответствует состоянию хранения")
		}
		key.revision = response.StateRevision
		if seen[key] {
			return errors.New("Повтор ревизии квитанции в копии")
		}
		seen[key] = true
	}
	return rows.Err()
}

func validExtensionReceiptBody(response *pb.ExecuteExtensionCommandResponse, retired bool) bool {
	if retired {
		return proto.Equal(response, &pb.ExecuteExtensionCommandResponse{Effect: response.Effect, StateRevision: response.StateRevision})
	}
	return len(response.DocumentJson) <= extensions.MaxJSON && extensions.CheckJSON(response.DocumentJson) == nil
}

func checkExtensionBackupCursor(ctx context.Context, tx pgx.Tx) error {
	var invalid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM extension_p1_delivery d LEFT JOIN extension_p1_instances i USING(installation,instance)
 WHERE i.instance IS NULL OR NOT ((d.ack_revision=0 AND d.ack_ordinal=-1) OR EXISTS(
 SELECT 1 FROM extension_p1_outbox o WHERE o.installation=d.installation AND o.instance=d.instance AND o.revision=d.ack_revision AND o.ordinal=d.ack_ordinal))
 OR (d.lease_token<>'' AND (d.lease_until IS NULL OR NOT EXISTS(
 SELECT 1 FROM extension_p1_outbox o WHERE o.installation=d.installation AND o.instance=d.instance AND o.revision=d.lease_revision AND o.ordinal=d.lease_ordinal AND (o.revision,o.ordinal)>(d.ack_revision,d.ack_ordinal)))))`).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid {
		return errors.New("Курсор доставки не соответствует outbox в копии")
	}
	return nil
}

func validExtensionReceiptVersion(ctx context.Context, tx pgx.Tx) (bool, error) {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT count(*)=1 AND CASE min(version) WHEN 1 THEN NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='extension_p1_receipts' AND column_name='retired') WHEN 2 THEN EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='extension_p1_receipts' AND column_name='retired' AND data_type='boolean' AND is_nullable='NO') ELSE false END FROM extension_p1_meta`).Scan(&valid)
	return valid, err
}
