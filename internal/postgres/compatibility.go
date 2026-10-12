package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

const HomePostgresMajor = 17

type Compatibility struct {
	SchemaVersion       int    `json:"schemaVersion"`
	TargetSchemaVersion int    `json:"targetSchemaVersion"`
	PostgresMajor       int    `json:"postgresMajor"`
	ServerID            string `json:"serverId"`
}

// Проверяем существующую базу только чтением, без Open и миграций.
func CheckCompatibility(ctx context.Context, databaseURL string) (Compatibility, error) {
	var result Compatibility
	if strings.TrimSpace(databaseURL) == "" {
		return result, errors.New("Для проверки нужна явная настройка SPACE_DATABASE_URL")
	}
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return result, errors.New("Не удалось подключиться для проверки совместимости")
	}
	defer conn.Close(ctx)
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, errors.New("Не удалось начать проверку только для чтения")
	}
	defer tx.Rollback(ctx)
	var version int
	if err := tx.QueryRow(ctx, "SELECT current_setting('server_version_num')::integer").Scan(&version); err != nil {
		return result, errors.New("Не удалось прочитать версию PostgreSQL")
	}
	result.PostgresMajor = version / 10000
	if result.PostgresMajor != HomePostgresMajor {
		return result, errors.New("Смена основной версии PostgreSQL требует отдельного переноса базы")
	}
	rows, err := tx.Query(ctx, "SELECT version,checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return result, errors.New("Нет доступной истории миграций существующей базы")
	}
	defer rows.Close()
	for rows.Next() {
		var current int
		var saved string
		if err := rows.Scan(&current, &saved); err != nil {
			return result, errors.New("Некорректная история миграций")
		}
		if current != result.SchemaVersion+1 || current > SchemaVersion() {
			return result, errors.New("Несовместимая или неполная история миграций")
		}
		sql, err := migrations.ReadFile("migrations/" + migrationFiles[current-1])
		if err != nil {
			return result, err
		}
		hash := sha256.Sum256([]byte(strings.ReplaceAll(string(sql), "\r\n", "\n")))
		if saved != hex.EncodeToString(hash[:]) {
			return result, errors.New("Контрольная сумма применённой миграции изменилась")
		}
		result.SchemaVersion = current
	}
	if err := rows.Err(); err != nil {
		return result, errors.New("Не удалось прочитать историю миграций")
	}
	if result.SchemaVersion == 0 {
		return result, errors.New("База не является существующей установкой Space")
	}
	identity, err := loadIdentity(ctx, tx, false)
	if err != nil {
		return result, errors.New("Не удалось проверить идентичность существующего сервера")
	}
	result.ServerID = identity.ServerID
	result.TargetSchemaVersion = SchemaVersion()
	return result, tx.Commit(ctx)
}
