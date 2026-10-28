package home

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackupDestinationBoundaries(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{source, filepath.Join(source, "nested", "copy"), filepath.Join(source, "..", "source", "copy")} {
		if err := outside(source, destination); err == nil {
			t.Fatalf("Копия внутри исходного каталога разрешена: %s", destination)
		}
	}
	if err := outside(source, filepath.Join(root, "another", "nested", "copy")); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Skipf("Нет права создания ссылки: %v", err)
	}
	if err := outside(source, filepath.Join(alias, "copy")); err == nil {
		t.Fatal("Ссылка обошла границу исходного каталога")
	}
	if err := outside(alias, filepath.Join(source, "nested", "copy")); err == nil {
		t.Fatal("Исходная ссылка обошла границу каталога")
	}
}
