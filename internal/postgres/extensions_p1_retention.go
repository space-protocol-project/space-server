package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/protobuf/proto"
)

// UpgradeExtensionP1Receipts вызывается только обслуживанием остановленного
// пространства после сохранения проверенной копии. Рабочего RPC для него нет.
func UpgradeExtensionP1Receipts(ctx context.Context, address string) error {
	_, err := maintainExtensionReceipts(ctx, address, true)
	return err
}

// RetireExtensionP1Receipts удаляет тела максимум 128 ответов старше 30 суток.
// Требует остановленного пространства; ключи, digest и ревизии сохраняются.
// Автоматический планировщик и рабочий сервер эту функцию не вызывают.
func RetireExtensionP1Receipts(ctx context.Context, address string) (int, error) {
	return maintainExtensionReceipts(ctx, address, false)
}

func maintainExtensionReceipts(ctx context.Context, address string, upgrade bool) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, address)
	if err != nil {
		return 0, err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='1s'; LOCK TABLE extension_p1_meta,extension_p1_instances,extension_p1_receipts IN ACCESS EXCLUSIVE MODE`); err != nil {
		return 0, err
	}
	tables, err := extensionBackupTables(ctx, tx)
	if err != nil {
		return 0, err
	}
	var version int
	if err = tx.QueryRow(ctx, `SELECT version FROM extension_p1_meta WHERE singleton=true`).Scan(&version); err != nil {
		return 0, err
	}
	if version != 2 && !(upgrade && version == 1) {
		return 0, errors.New("Неизвестная версия хранения квитанций")
	}
	valid, err := validExtensionReceiptVersion(ctx, tx)
	if err != nil {
		return 0, err
	}
	if !valid {
		return 0, errors.New("Повреждена версия хранения квитанций")
	}
	if err = checkExtensionBackupRows(ctx, tx, tables["extension_p1_delivery"]); err != nil {
		return 0, err
	}
	count := 0
	if upgrade {
		err = upgradeExtensionReceipts(ctx, tx, version)
	} else {
		count, err = retireExtensionReceipts(ctx, tx)
	}
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return count, nil
}

func upgradeExtensionReceipts(ctx context.Context, tx pgx.Tx, version int) error {
	if version == 2 {
		return nil
	}
	_, err := tx.Exec(ctx, `ALTER TABLE extension_p1_receipts ADD COLUMN retired boolean NOT NULL DEFAULT false;
ALTER TABLE extension_p1_meta DROP CONSTRAINT extension_p1_meta_version_check;
ALTER TABLE extension_p1_meta ADD CONSTRAINT extension_p1_meta_version_check CHECK(version=2) NOT VALID;
UPDATE extension_p1_meta SET version=2;
ALTER TABLE extension_p1_meta VALIDATE CONSTRAINT extension_p1_meta_version_check`)
	return err
}

type retiredExtensionReceipt struct {
	installation, instance, principal, command, key string
	response                                        []byte
}

func retireExtensionReceipts(ctx context.Context, tx pgx.Tx) (int, error) {
	rows, err := tx.Query(ctx, `SELECT installation,instance,principal,command,key,response FROM extension_p1_receipts
WHERE NOT retired AND created_at <= transaction_timestamp()-interval '720 hours'
ORDER BY created_at,installation,instance,principal,command,key LIMIT 128 FOR UPDATE`)
	if err != nil {
		return 0, err
	}
	items, err := collectRetiredReceipts(rows)
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		_, err = tx.Exec(ctx, `UPDATE extension_p1_receipts SET response=$6,retired=true WHERE installation=$1 AND instance=$2 AND principal=$3 AND command=$4 AND key=$5`, item.installation, item.instance, item.principal, item.command, item.key, item.response)
		if err != nil {
			return 0, err
		}
	}
	return len(items), nil
}

func collectRetiredReceipts(rows pgx.Rows) ([]retiredExtensionReceipt, error) {
	defer rows.Close()
	var items []retiredExtensionReceipt
	for rows.Next() {
		var item retiredExtensionReceipt
		if err := rows.Scan(&item.installation, &item.instance, &item.principal, &item.command, &item.key, &item.response); err != nil {
			return nil, err
		}
		old := new(pb.ExecuteExtensionCommandResponse)
		if err := proto.Unmarshal(item.response, old); err != nil {
			return nil, err
		}
		// Создаём новый protobuf: удаляются также неизвестные поля старого ответа.
		stub := &pb.ExecuteExtensionCommandResponse{Effect: old.Effect, StateRevision: old.StateRevision}
		item.response, _ = proto.Marshal(stub)
		items = append(items, item)
	}
	return items, rows.Err()
}
