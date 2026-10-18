package home

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScheduledRetentionOptInAndMinimumThree(t *testing.T) {
	options, now, count := scheduledFixture(t)
	name := filepath.Join(filepath.Dir(options.Data), "backup-policy.json")
	policy := BackupPolicy{1, true, false, 24, 100, 1, 1}
	if err := writeBackupJSON(name, policy); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := RunScheduledBackupCycle(context.Background(), options); err != nil {
			t.Fatal(err)
		}
		if i < 4 {
			*now = now.Add(25 * time.Hour)
		}
	}
	if scheduledState(t, options).Count != 5 {
		t.Fatal("Удалены копии без включения очистки")
	}
	policy.Prune = true
	if err := writeBackupJSON(name, policy); err != nil {
		t.Fatal(err)
	}
	if err := RunScheduledBackupCycle(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	status := scheduledState(t, options)
	if status.Count != 3 || !status.Limited || *count != 5 {
		t.Fatalf("Не соблюдён минимум/объём: %+v, %d", status, *count)
	}
}

func TestScheduledMissingStatusUsesVerifiedReceipt(t *testing.T) {
	options, _, count := scheduledFixture(t)
	if err := RunScheduledBackupCycle(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(filepath.Dir(options.Data), "backup-status.json")); err != nil {
		t.Fatal(err)
	}
	if err := RunScheduledBackupCycle(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if *count != 1 || scheduledState(t, options).Count != 1 {
		t.Fatal("Потеря статуса создала дубликат")
	}
}

func TestScheduledFolderLinkIsProtected(t *testing.T) {
	options, _, count := scheduledFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(filepath.Dir(options.Data), "scheduled-backups")); err != nil {
		t.Skip("Нет прав на ссылку", err)
	}
	if err := RunScheduledBackupCycle(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if *count != 0 || scheduledState(t, options).Code != "failed" {
		t.Fatal("Каталог со ссылкой принят")
	}
	if value, err := os.ReadFile(filepath.Join(outside, "keep")); err != nil || string(value) != "keep" {
		t.Fatal("Внешние данные изменились")
	}
}
