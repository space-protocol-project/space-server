package home

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func scheduledFixture(t *testing.T) (ScheduledBackupOptions, *time.Time, *int) {
	t.Helper()
	root := t.TempDir()
	source := inspectionFixture(t, func(*portableManifest, map[string][]byte) {})
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	count := 0
	options := ScheduledBackupOptions{Data: filepath.Join(root, "data"), ServerID: "test-server", Clock: func() time.Time { return now }, Free: func(string) (uint64, error) { return 1 << 40, nil }, Size: func(context.Context, string) (uint64, error) { return 1024, nil }, Export: func(ctx context.Context, _, _, _, destination string) error {
		count++
		return copyFile(source, destination)
	}}
	policy := BackupPolicy{1, true, true, 24, 3, 30, 0}
	if err := writeBackupJSON(filepath.Join(root, "backup-policy.json"), policy); err != nil {
		t.Fatal(err)
	}
	return options, &now, &count
}

func scheduledState(t *testing.T, options ScheduledBackupOptions) BackupStatus {
	t.Helper()
	var status BackupStatus
	if err := readBackupJSON(filepath.Join(filepath.Dir(options.Data), "backup-status.json"), &status); err != nil {
		t.Fatal(err)
	}
	return status
}

// На Windows удалённый файл может ненадолго оставаться в перечислении каталога.
// Содержимое и число копий проверяем после завершения этой операции файловой системы.
func waitScheduledFileCount(t *testing.T, folder string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	stable := 0
	for {
		entries, err := os.ReadDir(folder)
		if err == nil && len(entries) == want {
			stable++
		} else {
			stable = 0
		}
		if stable == 2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Не сохранено нужное число файлов копий: %v %d вместо %d", err, len(entries), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestScheduledBackupsRestartRetention(t *testing.T) {
	options, now, count := scheduledFixture(t)
	for i := 0; i < 5; i++ {
		if err := RunScheduledBackupCycle(context.Background(), options); err != nil {
			t.Fatal(err)
		}
		if err := RunScheduledBackupCycle(context.Background(), options); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(25 * time.Hour)
	}
	if *count != 5 {
		t.Fatalf("Повтор или пропуск: %d", *count)
	}
	status := scheduledState(t, options)
	if status.Count != 3 || status.Code != "ok" || status.LastSuccessAt == 0 {
		t.Fatalf("Неверный статус: %+v", status)
	}
	folder := filepath.Join(filepath.Dir(options.Data), "scheduled-backups")
	waitScheduledFileCount(t, folder, 6)
	*now = now.Add(365 * 24 * time.Hour)
	if err := RunScheduledBackupCycle(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if *count != 6 {
		t.Fatal("После простоя создана очередь копий")
	}
	waitScheduledFileCount(t, folder, 6)
}

func TestScheduledBackupsSpaceAndFailedExportPreserveHistory(t *testing.T) {
	options, now, count := scheduledFixture(t)
	if err := RunScheduledBackupCycle(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	success := scheduledState(t, options).LastSuccessAt
	*now = now.Add(25 * time.Hour)
	options.Free = func(string) (uint64, error) { return 0, nil }
	if err := RunScheduledBackupCycle(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	status := scheduledState(t, options)
	if status.Code != "space_low" || status.LastSuccessAt != success || *count != 1 {
		t.Fatalf("Потеря статуса/запуск без места: %+v", status)
	}
	if err := RunScheduledBackupCycle(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(6 * time.Minute)
	options.Free = func(string) (uint64, error) { return 1 << 40, nil }
	options.Export = func(context.Context, string, string, string, string) error { return errors.New("secret-password") }
	if err := RunScheduledBackupCycle(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	status = scheduledState(t, options)
	if status.Code != "failed" || status.LastSuccessAt != success {
		t.Fatalf("Ошибка объявлена успехом: %+v", status)
	}
	raw, _ := os.ReadFile(filepath.Join(filepath.Dir(options.Data), "backup-status.json"))
	if strings.Contains(string(raw), "secret-password") {
		t.Fatal("Секрет попал в статус")
	}
}

func TestScheduledBackupsProtectUnknownCorruptAndLinks(t *testing.T) {
	options, now, _ := scheduledFixture(t)
	folder, err := scheduledFolder(filepath.Dir(options.Data))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := RunScheduledBackupCycle(context.Background(), options); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(25 * time.Hour)
	}
	if err := os.WriteFile(filepath.Join(folder, "manual.zip"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	items, _, err := listScheduled(context.Background(), folder, options.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	changed := filepath.Join(folder, items[0].Name)
	if err := os.WriteFile(changed, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RunScheduledBackupCycle(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(changed); err != nil {
		t.Fatal("Повреждённый архив удалён")
	}
	if _, err := os.Stat(filepath.Join(folder, "manual.zip")); err != nil {
		t.Fatal("Чужой архив удалён")
	}
	if scheduledState(t, options).Protected < 2 {
		t.Fatal("Защищённые файлы скрыты")
	}
}

func TestScheduledDisabledAndInvalidPolicy(t *testing.T) {
	options, _, count := scheduledFixture(t)
	name := filepath.Join(filepath.Dir(options.Data), "backup-policy.json")
	if err := writeBackupJSON(name, BackupPolicy{1, false, false, 24, 7, 30, 0}); err != nil {
		t.Fatal(err)
	}
	if err := RunScheduledBackupCycle(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if *count != 0 || scheduledState(t, options).Code != "disabled" {
		t.Fatal("Выключенное расписание запущено")
	}
	if err := os.WriteFile(name, []byte(`{"format":1,"enabled":true,"intervalHours":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RunScheduledBackupCycle(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if *count != 0 || scheduledState(t, options).Code != "invalid_policy" {
		t.Fatal("Повреждённое расписание запущено")
	}
}
