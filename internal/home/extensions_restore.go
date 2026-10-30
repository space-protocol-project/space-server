package home

import (
	"context"
	"errors"

	"github.com/space-protocol-project/space-server/internal/postgres"
)

// Проверяется только временная восстановленная копия, до публикации назначения.
// PostgreSQL останавливается и при ошибке проверки; исходный архив не меняется.
func prepareRestoredExtensions(runtime, data string) (result error) {
	address, stop, err := StartDatabase(runtime, data)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, stop()) }()
	return postgres.CheckExtensionP1Backup(context.Background(), address, true)
}
