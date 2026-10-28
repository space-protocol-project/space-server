package home

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBackupWindowsVolumes(t *testing.T) {
	for _, pair := range [][2]string{{`C:\space\data`, `D:\copy.zip`}, {`C:\space\data`, `c:\space\other\copy.zip`}, {`\\server\one\data`, `\\server\two\copy.zip`}} {
		if err := outsidePaths(pair[0], pair[1]); err != nil {
			t.Fatalf("Допустимое назначение: %v: %v", pair, err)
		}
	}
	if err := outsidePaths(`C:\space\data`, `c:\SPACE\DATA\copy.zip`); err == nil {
		t.Fatal("Регистр буквы диска обошёл границу")
	}
}

func TestBackupRejectsSubstAlias(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Skip("Временное сопоставление диска создаётся только в CI")
	}
	if _, err := os.Stat(`Z:\`); !os.IsNotExist(err) {
		t.Skip("Буква тестового диска занята")
	}
	source := t.TempDir()
	child := filepath.Join(source, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("subst", "Z:", child).CombinedOutput(); err != nil {
		t.Fatalf("SUBST: %v: %s", err, output)
	}
	t.Cleanup(func() {
		if output, err := exec.Command("subst", "Z:", "/D").CombinedOutput(); err != nil {
			t.Errorf("Удаление SUBST: %v: %s", err, output)
		}
	})
	if err := outside(source, `Z:\copy.zip`); err == nil {
		t.Fatal("SUBST позволил сохранить копию внутри исходного каталога")
	}
}
