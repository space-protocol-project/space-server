package home

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/space-protocol-project/space-server/internal/postgres"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type BackupManifest struct {
	Format        int               `json:"format"`
	OS            string            `json:"os"`
	Arch          string            `json:"arch"`
	PostgresMajor int               `json:"postgresMajor"`
	CreatedAt     string            `json:"createdAt"`
	Files         map[string]string `json:"files"`
	Directories   []string          `json:"directories"`
}

func CheckStoppedDatabase(runtime, data string) error {
	if err := CheckDatabaseVersion(runtime, data); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(data, "postgres", "postmaster.pid")); !os.IsNotExist(err) {
		return errors.New("База работает либо её остановка не подтверждена")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	base := command(runtime, "pg_controldata", filepath.Join(data, "postgres"))
	cmd := exec.CommandContext(ctx, base.Path, base.Args[1:]...)
	cmd.Env = append(base.Env, "LC_ALL=C", "LANG=C")
	hideWindow(cmd)
	output, err := cmd.Output()
	if err != nil || !regexp.MustCompile(`(?m)^Database cluster state:\s+shut down\s*$`).Match(output) {
		return errors.New("Нужна штатно остановленная PostgreSQL перед копированием")
	}
	return nil
}

func outside(source, destination string) error {
	from, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	to, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if err := outsidePaths(from, to); err != nil {
		return err
	}
	from, err = canonicalBackupPath(from)
	if err != nil {
		return err
	}
	to, err = canonicalBackupPath(to)
	if err != nil {
		return err
	}
	return outsidePaths(from, to)
}

func outsidePaths(from, to string) error {
	if !strings.EqualFold(filepath.VolumeName(from), filepath.VolumeName(to)) {
		return nil
	}
	relative, err := filepath.Rel(from, to)
	if err != nil {
		return err
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return errors.New("Копия должна находиться вне исходного каталога")
	}
	return nil
}

// Разрешаем существующий предок, не создавая назначение. Неразрешимые ссылки
// и ошибки доступа отклоняются; ещё не созданный хвост сохраняется.
func canonicalBackupPath(path string) (string, error) {
	var tail []string
	for {
		_, err := os.Lstat(path)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		tail = append([]string{filepath.Base(path)}, tail...)
		path = parent
	}
	resolved, err := finalBackupPath(path)
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{resolved}, tail...)...), nil
}

// Не следуем ссылкам: tablespace и внешние файлы требуют отдельного сценария.
func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return errors.New("Ссылки и специальные файлы в резервной копии запрещены")
		}
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		return copyFile(path, target)
	})
}

func copyFile(source, destination string) (result error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, output.Close()) }()
	if _, err = io.Copy(output, input); err != nil {
		return err
	}
	return output.Sync()
}

func inventory(root string) (BackupManifest, error) {
	manifest := BackupManifest{Format: 1, OS: runtime.GOOS, Arch: runtime.GOARCH, PostgresMajor: postgres.HomePostgresMajor, Files: map[string]string{}, Directories: []string{}}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkError error) error {
		return manifest.addEntry(root, path, entry, walkError)
	})
	return manifest, err
}

func (manifest *BackupManifest) addEntry(root, path string, entry fs.DirEntry, walkError error) error {
	if walkError != nil {
		return walkError
	}
	if path == root {
		return nil
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	name := filepath.ToSlash(relative)
	if name == "backup.json" {
		return nil
	}
	info, err := entry.Info()
	if err != nil {
		return err
	}
	if entry.Type()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return errors.New("Неподдерживаемый файл копии")
	}
	if len(manifest.Files)+len(manifest.Directories) >= 100000 {
		return errors.New("Слишком много файлов копии")
	}
	if info.IsDir() {
		manifest.Directories = append(manifest.Directories, name)
		return nil
	}
	digest, err := backupFileDigest(path)
	if err != nil {
		return err
	}
	manifest.Files[name] = digest
	return nil
}

func backupFileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	closeErr := file.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// SHA-256 обнаруживает повреждение; это не подпись от подмены копии.
func VerifyBackup(root string) error {
	file, err := os.Lstat(filepath.Join(root, "backup.json"))
	if err != nil {
		return err
	}
	if !file.Mode().IsRegular() || file.Size() > 16*1024*1024 {
		return errors.New("Недопустимый манифест копии")
	}
	bytes, err := os.ReadFile(filepath.Join(root, "backup.json"))
	if err != nil {
		return err
	}
	var saved BackupManifest
	if err := json.Unmarshal(bytes, &saved); err != nil || saved.Format != 1 {
		return errors.New("Неизвестный формат копии")
	}
	if saved.OS != runtime.GOOS || saved.Arch != runtime.GOARCH || saved.PostgresMajor != postgres.HomePostgresMajor {
		return errors.New("Копия относится к другой платформе или основной версии PostgreSQL")
	}
	actual, err := inventory(root)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(saved.Files, actual.Files) || !reflect.DeepEqual(saved.Directories, actual.Directories) {
		return errors.New("Резервная копия повреждена или неполна")
	}
	for _, name := range []string{"data/postgres/PG_VERSION", "data/database-password"} {
		if _, ok := actual.Files[name]; !ok {
			return fmt.Errorf("В копии отсутствует %s", name)
		}
	}
	return nil
}

// Вызывается локальным управляющим процессом под блокировкой запуска/остановки.
func CreateBackup(runtime, data, destination string) error {
	if err := outside(data, destination); err != nil {
		return err
	}
	if err := CheckStoppedDatabase(runtime, data); err != nil {
		return err
	}
	return publishDirectory(destination, func(staging string) error {
		if err := copyTree(data, filepath.Join(staging, "data")); err != nil {
			return err
		}
		if err := copyBackupConfiguration(filepath.Dir(data), staging); err != nil {
			return err
		}
		if err := CheckStoppedDatabase(runtime, data); err != nil {
			return err
		}
		if err := writeBackupManifest(staging); err != nil {
			return err
		}
		if err := VerifyBackup(staging); err != nil {
			return err
		}
		return CheckStoppedDatabase(runtime, filepath.Join(staging, "data"))
	})
}

func copyBackupConfiguration(root, staging string) error {
	for _, name := range []string{"config.json", "active-runtime.json"} {
		source := filepath.Join(root, name)
		info, err := os.Lstat(source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("Конфигурация не является обычным файлом")
		}
		if err := copyFile(source, filepath.Join(staging, name)); err != nil {
			return err
		}
	}
	return nil
}

func writeBackupManifest(staging string) error {
	manifest, err := inventory(staging)
	if err != nil {
		return err
	}
	manifest.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	bytes, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(staging, "backup.json"), bytes, 0600)
}

// Восстанавливаем только в новый каталог; действующие данные не заменяем.
func RestoreBackup(runtime, source, destination string) error {
	if err := outside(source, destination); err != nil {
		return err
	}
	if err := VerifyBackup(source); err != nil {
		return err
	}
	if err := CheckStoppedDatabase(runtime, filepath.Join(source, "data")); err != nil {
		return err
	}
	return publishDirectory(destination, func(staging string) error {
		if err := copyTree(source, staging); err != nil {
			return err
		}
		if err := VerifyBackup(staging); err != nil {
			return err
		}
		return CheckStoppedDatabase(runtime, filepath.Join(staging, "data"))
	})
}

func publishDirectory(destination string, prepare func(string) error) error {
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return errors.New("Каталог назначения уже существует или недоступен")
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".space-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging) // Только созданный нами временный каталог.
	if err := protectBackupDirectory(staging); err != nil {
		return err
	}
	if err := prepare(staging); err != nil {
		return err
	}
	return renamePublishedDirectory(staging, destination)
}
