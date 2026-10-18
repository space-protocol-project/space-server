package home

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/space-protocol-project/space-server/internal/postgres"
)

// PortableInspection не содержит ключей, конфигурации и содержимого сообщений.
type PortableInspection struct {
	Format        int    `json:"format"`
	SchemaVersion int    `json:"schemaVersion"`
	ServerID      string `json:"serverId"`
	Files         int    `json:"files"`
	UnpackedBytes uint64 `json:"unpackedBytes"`
}

// InspectPortable проверяет архив потоково, не распаковывая и не запуская БД.
// Контрольные суммы проверяют целостность, но не подлинность источника архива.
func InspectPortable(source string) (*PortableInspection, error) {
	file, err := os.Open(source)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxPortableBytes+(1<<20) {
		return nil, errors.New("Недопустимый размер архива переноса")
	}
	archive, err := zip.NewReader(file, info.Size())
	if err != nil {
		return nil, err
	}
	return inspectPortableEntries(archive.File)
}

func inspectPortableEntries(entries []*zip.File) (*PortableInspection, error) {
	if len(entries) != 5 {
		return nil, errors.New("Неверный состав архива переноса")
	}
	seen, hashes := map[string]bool{}, map[string]string{}
	var manifest portableManifest
	var total uint64
	for _, entry := range entries {
		if !portableEntryAllowed(entry, seen) || entry.UncompressedSize64 > uint64(maxPortableBytes)-total {
			return nil, errors.New("Недопустимый элемент или размер архива переноса")
		}
		seen[entry.Name] = true
		total += entry.UncompressedSize64
		digest, err := inspectPortableEntry(entry, &manifest)
		if err != nil {
			return nil, err
		}
		hashes[entry.Name] = digest
	}
	if manifest.Format != 1 || manifest.SchemaVersion != postgres.SchemaVersion() || manifest.ServerID == "" || len(manifest.Files) != 4 {
		return nil, errors.New("Несовместимая версия архива переноса")
	}
	for _, name := range portableNames {
		if manifest.Files[name] != hashes[name] {
			return nil, errors.New("Контрольная сумма архива переноса не совпадает")
		}
	}
	return &PortableInspection{manifest.Format, manifest.SchemaVersion, manifest.ServerID, len(entries), total}, nil
}

func inspectPortableEntry(entry *zip.File, manifest *portableManifest) (string, error) {
	input, err := entry.Open()
	if err != nil {
		return "", err
	}
	defer input.Close()
	if entry.Name == "space-export.json" {
		if entry.UncompressedSize64 > 16384 {
			return "", errors.New("Манифест переноса слишком большой")
		}
		data, err := io.ReadAll(io.LimitReader(input, 16385))
		if err != nil {
			return "", err
		}
		return "", json.Unmarshal(data, manifest)
	}
	digest := sha256.New()
	count, err := io.Copy(digest, io.LimitReader(input, int64(entry.UncompressedSize64)+1))
	if err != nil {
		return "", err
	}
	if uint64(count) != entry.UncompressedSize64 {
		return "", errors.New("Неверный размер файла переноса")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
