package home

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

var scheduledName = regexp.MustCompile(`^archive-[0-9]{13}-[a-f0-9]{32}\.zip$`)

type scheduledArchive struct {
	Format    int    `json:"format"`
	Name      string `json:"name"`
	ServerID  string `json:"serverId"`
	CreatedAt int64  `json:"createdAt"`
	Bytes     uint64 `json:"bytes"`
	SHA256    string `json:"sha256"`
}

func scheduledFolder(root string) (string, error) {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	folder := filepath.Join(canonical, "scheduled-backups")
	if err := os.Mkdir(folder, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err := os.Lstat(folder)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("Каталог копий содержит ссылку")
	}
	resolved, err := filepath.EvalSymlinks(folder)
	if err != nil || filepath.Dir(resolved) != canonical {
		return "", errors.New("Недопустимый каталог копий")
	}
	return folder, protectBackupDirectory(folder)
}

func scheduledDigest(ctx context.Context, name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := file.Read(buffer)
		if n > 0 {
			hash.Write(buffer[:n])
		}
		if err == io.EOF {
			return hex.EncodeToString(hash.Sum(nil)), nil
		}
		if err != nil {
			return "", err
		}
	}
}

func verifyScheduled(ctx context.Context, folder, serverID string, item scheduledArchive) error {
	if item.Format != 1 || !scheduledName.MatchString(item.Name) || item.ServerID != serverID || item.CreatedAt <= 0 || len(item.SHA256) != 64 {
		return errors.New("Некорректная квитанция копии")
	}
	name := filepath.Join(folder, item.Name)
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || uint64(info.Size()) != item.Bytes {
		return errors.New("Копия отсутствует или изменена")
	}
	digest, err := scheduledDigest(ctx, name)
	if err != nil || digest != item.SHA256 {
		return errors.New("Копия повреждена")
	}
	report, err := InspectPortable(name)
	if err != nil || report.ServerID != serverID {
		return errors.New("Архив копии несовместим")
	}
	return nil
}

func listScheduled(ctx context.Context, folder, serverID string) ([]scheduledArchive, int, error) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil, 0, err
	}
	if len(entries) > 10000 {
		return nil, 0, errors.New("Слишком много файлов копий")
	}
	items := []scheduledArchive{}
	known := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if !scheduledName.MatchString(name) {
			continue
		}
		var item scheduledArchive
		if readBackupJSON(filepath.Join(folder, name+".json"), &item) != nil || item.Name != name {
			continue
		}
		if err := verifyScheduled(ctx, folder, serverID, item); err != nil {
			if ctx.Err() != nil {
				return nil, 0, ctx.Err()
			}
			continue
		}
		items = append(items, item)
		known[name], known[name+".json"] = true, true
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt != items[j].CreatedAt {
			return items[i].CreatedAt > items[j].CreatedAt
		}
		return items[i].Name > items[j].Name
	})
	return items, len(entries) - len(known), nil
}

func publishScheduled(ctx context.Context, folder, serverID string, now time.Time, export func(string) error) (scheduledArchive, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return scheduledArchive{}, err
	}
	name := fmt.Sprintf("archive-%013d-%x.zip", now.UnixMilli(), nonce)
	file := filepath.Join(folder, name)
	if err := export(file); err != nil {
		return scheduledArchive{}, err
	}
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() {
		return scheduledArchive{}, errors.New("Не удалось проверить копию")
	}
	digest, err := scheduledDigest(ctx, file)
	if err != nil {
		return scheduledArchive{}, err
	}
	item := scheduledArchive{1, name, serverID, now.UnixMilli(), uint64(info.Size()), digest}
	if err := verifyScheduled(ctx, folder, serverID, item); err != nil {
		return scheduledArchive{}, err
	}
	if err := writeBackupJSON(file+".json", item); err != nil {
		return scheduledArchive{}, err
	}
	return item, nil
}

func pruneScheduled(ctx context.Context, folder, serverID string, policy BackupPolicy, now time.Time, items []scheduledArchive) ([]scheduledArchive, error) {
	if !policy.Prune || len(items) <= 3 {
		return items, nil
	}
	for _, item := range items[:3] {
		if err := verifyScheduled(ctx, folder, serverID, item); err != nil {
			return items, err
		}
	}
	var total uint64
	for _, item := range items {
		total += item.Bytes
	}
	for len(items) > 3 {
		last := items[len(items)-1]
		tooOld := now.Sub(time.UnixMilli(last.CreatedAt)) > time.Duration(policy.KeepDays)*24*time.Hour
		if !tooOld && len(items) <= policy.KeepCount && (policy.MaxBytes == 0 || total <= policy.MaxBytes) {
			break
		}
		if err := verifyScheduled(ctx, folder, serverID, last); err != nil {
			return items, err
		}
		if err := os.Remove(filepath.Join(folder, last.Name)); err != nil {
			return items, err
		}
		if err := os.Remove(filepath.Join(folder, last.Name+".json")); err != nil {
			return items, err
		}
		total -= last.Bytes
		items = items[:len(items)-1]
	}
	return items, nil
}
