package home

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/space-protocol-project/space-server/internal/postgres"
)

var postgresVersionPattern = regexp.MustCompile(`^postgres \(PostgreSQL\) ([0-9]+)\.[0-9]+`)

func validatePostgresVersions(binaryVersion, clusterVersion string) error {
	match := postgresVersionPattern.FindStringSubmatch(strings.TrimSpace(binaryVersion))
	if len(match) != 2 || match[1] != strconv.Itoa(postgres.HomePostgresMajor) {
		return fmt.Errorf("Комплект PostgreSQL не подходит домашнему серверу")
	}
	if clusterVersion != "" && strings.TrimSpace(clusterVersion) != match[1] {
		return fmt.Errorf("Смена основной версии PostgreSQL требует отдельного переноса базы")
	}
	return nil
}

// Проверяем бинарный файл и PG_VERSION до записи конфигурации и запуска базы.
func CheckDatabaseVersion(root, data string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	base := command(root, "postgres", "--version")
	cmd := exec.CommandContext(ctx, base.Path, base.Args[1:]...)
	cmd.Env = base.Env
	hideWindow(cmd)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("Не удалось проверить бинарный файл PostgreSQL")
	}
	versionPath := filepath.Join(data, "postgres", "PG_VERSION")
	version, err := os.ReadFile(versionPath)
	if os.IsNotExist(err) {
		if _, statErr := os.Stat(filepath.Dir(versionPath)); !os.IsNotExist(statErr) {
			return fmt.Errorf("Каталог существующей базы не содержит PG_VERSION; требуется восстановление")
		}
	} else if err != nil {
		return fmt.Errorf("Не удалось прочитать версию существующей базы")
	} else if strings.TrimSpace(string(version)) == "" {
		return fmt.Errorf("Пустая версия существующей базы; требуется восстановление")
	}
	if len(version) > 16 {
		return fmt.Errorf("Некорректная версия существующей базы")
	}
	return validatePostgresVersions(string(output), string(version))
}
