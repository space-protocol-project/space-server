package home

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"time"

	"github.com/space-protocol-project/space-server/internal/postgres"
)

const maxPortableBytes int64 = 2 << 30

var portableNames = []string{"database.dump", "config.json", "server.crt", "server.key"}

type portableManifest struct {
	Format        int               `json:"format"`
	SchemaVersion int               `json:"schemaVersion"`
	ServerID      string            `json:"serverId"`
	Files         map[string]string `json:"files"`
}

// Только остановленный сервер; пароль базы и физические файлы PostgreSQL не переносятся.
func ExportPortable(runtime, data, destination string) error {
	if err := outside(data, destination); err != nil {
		return err
	}
	if err := CheckStoppedDatabase(runtime, data); err != nil {
		return err
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return errors.New("Файл экспорта уже существует или недоступен")
	}
	staging, err := os.MkdirTemp(filepath.Dir(data), ".space-export-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := protectBackupDirectory(staging); err != nil {
		return err
	}
	if err := preparePortableDump(runtime, data, staging); err != nil {
		return err
	}
	return writePortableArchive(staging, destination)
}

func preparePortableDump(runtime, data, staging string) (result error) {
	address, stop, err := StartDatabase(runtime, data)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, stop()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	identity, err := postgres.CheckCompatibility(ctx, address)
	if err != nil {
		return err
	}
	if err := portableDatabaseTool(runtime, "pg_dump", address, staging, "--format=custom", "--no-owner", "--no-privileges", "--exclude-table-data=auth_sessions", "--file="+filepath.Join(staging, "database.dump")); err != nil {
		return err
	}
	if err := copyFile(filepath.Join(filepath.Dir(data), "config.json"), filepath.Join(staging, "config.json")); err != nil {
		return err
	}
	for _, name := range []string{"server.crt", "server.key"} {
		if err := copyFile(filepath.Join(data, name), filepath.Join(staging, name)); err != nil {
			return err
		}
	}
	manifest := portableManifest{1, identity.SchemaVersion, identity.ServerID, map[string]string{}}
	for _, name := range portableNames {
		hash, err := backupFileDigest(filepath.Join(staging, name))
		if err != nil {
			return err
		}
		manifest.Files[name] = hash
	}
	bytes, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(staging, "space-export.json"), bytes, 0600)
}

func portableDatabaseTool(runtime, tool, address, logDir string, arguments ...string) error {
	parsed, err := url.Parse(address)
	if err != nil {
		return err
	}
	password, _ := parsed.User.Password()
	parsed.User = url.User(parsed.User.Username())
	base := command(runtime, tool, append([]string{"--dbname=" + parsed.String()}, arguments...)...)
	base.Env = append(base.Env, "PGPASSWORD="+password)
	// Большой сервер может обрабатываться дольше обычной проверки запуска.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	base = commandWithContext(ctx, base)
	output, err := runPortableLogged(base, logDir)
	if err != nil {
		return fmt.Errorf("%s: %w: %s", tool, err, output)
	}
	return nil
}

func writePortableArchive(staging, destination string) (result error) {
	if err := checkPortableSize(staging); err != nil {
		return err
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if goruntime.GOOS == "windows" {
		if err := protectBackupDirectory(destination); err != nil {
			file.Close()
			os.Remove(destination)
			return err
		}
	}
	defer func() {
		result = errors.Join(result, file.Close())
		if result != nil {
			_ = os.Remove(destination)
		}
	}()
	writer := zip.NewWriter(file)
	for _, name := range append(append([]string{}, portableNames...), "space-export.json") {
		if err := appendPortableFile(writer, staging, name); err != nil {
			_ = writer.Close()
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return file.Sync()
}

func appendPortableFile(writer *zip.Writer, staging, name string) error {
	source, err := os.Open(filepath.Join(staging, name))
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maxPortableBytes {
		return errors.New("Экспорт превышает 2 ГиБ")
	}
	target, err := writer.Create(name)
	if err != nil {
		return err
	}
	_, err = io.Copy(target, source)
	return err
}

// Архив проверяется до создания базы. Действующий каталог никогда не заменяется.
func ImportPortable(runtime, source, destination, publicIP string) error {
	if err := validatePortableIP(publicIP); err != nil {
		return err
	}
	return publishDirectory(destination, func(staging string) error {
		unpacked, err := os.MkdirTemp(staging, ".import-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(unpacked)
		if err := extractPortableArchive(source, unpacked); err != nil {
			return err
		}
		manifest, err := verifyPortableManifest(unpacked)
		if err != nil {
			return err
		}
		if err := restorePortableDatabase(runtime, staging, unpacked, manifest); err != nil {
			return err
		}
		if err := setPortableAddress(staging, publicIP); err != nil {
			return err
		}
		digest, err := backupFileDigest(source)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(staging, "import-source.sha256"), []byte(digest), 0600)
	})
}

func extractPortableArchive(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxPortableBytes+(1<<20) {
		return errors.New("Архив переноса превышает 2 ГиБ")
	}
	archive, err := zip.OpenReader(source)
	if err != nil {
		return err
	}
	defer archive.Close()
	if len(archive.File) != 5 {
		return errors.New("Неверный состав архива переноса")
	}
	seen := map[string]bool{}
	var total uint64
	for _, entry := range archive.File {
		if !portableEntryAllowed(entry, seen) {
			return errors.New("Недопустимый элемент архива переноса")
		}
		seen[entry.Name] = true
		total += entry.UncompressedSize64
		if total > uint64(maxPortableBytes) {
			return errors.New("Архив переноса превышает 2 ГиБ")
		}
		if err := extractPortableFile(entry, filepath.Join(destination, entry.Name)); err != nil {
			return err
		}
	}
	return nil
}

func portableEntryAllowed(entry *zip.File, seen map[string]bool) bool {
	if seen[entry.Name] || !entry.Mode().IsRegular() {
		return false
	}
	if entry.Name != "database.dump" && entry.UncompressedSize64 > 64*1024 {
		return false
	}
	for _, name := range append(append([]string{}, portableNames...), "space-export.json") {
		if entry.Name == name {
			return true
		}
	}
	return false
}

func extractPortableFile(entry *zip.File, destination string) (result error) {
	input, err := entry.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, output.Close()) }()
	count, err := io.Copy(output, io.LimitReader(input, maxPortableBytes+1))
	if err != nil {
		return err
	}
	if count > maxPortableBytes || uint64(count) != entry.UncompressedSize64 {
		return errors.New("Неверный размер файла переноса")
	}
	return output.Sync()
}

func verifyPortableManifest(directory string) (*portableManifest, error) {
	file := filepath.Join(directory, "space-export.json")
	info, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if info.Size() > 16384 {
		return nil, errors.New("Манифест переноса слишком большой")
	}
	bytes, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var manifest portableManifest
	if err := json.Unmarshal(bytes, &manifest); err != nil {
		return nil, err
	}
	if manifest.Format != 1 || manifest.SchemaVersion != postgres.SchemaVersion() || len(manifest.Files) != 4 || manifest.ServerID == "" {
		return nil, errors.New("Несовместимая версия архива переноса")
	}
	for _, name := range portableNames {
		digest, err := backupFileDigest(filepath.Join(directory, name))
		if err != nil {
			return nil, err
		}
		if manifest.Files[name] != digest {
			return nil, errors.New("Контрольная сумма архива переноса не совпадает")
		}
	}
	return &manifest, nil
}

func restorePortableDatabase(runtime, staging, unpacked string, manifest *portableManifest) (result error) {
	data := filepath.Join(staging, "data")
	address, stop, err := StartDatabase(runtime, data)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, stop()) }()
	if err := portableDatabaseTool(runtime, "pg_restore", address, data, "--single-transaction", "--exit-on-error", "--no-owner", "--no-privileges", filepath.Join(unpacked, "database.dump")); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	identity, err := postgres.CheckCompatibility(ctx, address)
	if err != nil {
		return err
	}
	if identity.ServerID != manifest.ServerID || identity.SchemaVersion != manifest.SchemaVersion {
		return errors.New("Идентичность импортированного пространства не совпадает")
	}
	if err := copyFile(filepath.Join(unpacked, "config.json"), filepath.Join(staging, "config.json")); err != nil {
		return err
	}
	for _, name := range []string{"server.crt", "server.key"} {
		if err := copyFile(filepath.Join(unpacked, name), filepath.Join(data, name)); err != nil {
			return err
		}
	}
	return nil
}

func commandWithContext(ctx context.Context, base *exec.Cmd) *exec.Cmd {
	result := exec.CommandContext(ctx, base.Path, base.Args[1:]...)
	result.Env = base.Env
	hideWindow(result)
	return result
}
func runPortableLogged(cmd *exec.Cmd, directory string) ([]byte, error) {
	path := filepath.Join(directory, "transfer.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	cmd.Stdout = file
	cmd.Stderr = file
	err = cmd.Run()
	file.Close()
	output, _ := os.ReadFile(path)
	if len(output) > 8192 {
		output = output[len(output)-8192:]
	}
	return output, err
}

func setPortableAddress(staging, publicIP string) error {
	if publicIP == "" {
		return nil
	}
	config := filepath.Join(staging, "config.json")
	content, err := os.ReadFile(config)
	if err != nil {
		return err
	}
	var network NetworkConfig
	if err := json.Unmarshal(content, &network); err != nil {
		return err
	}
	if network.IP == publicIP {
		return nil
	}
	if err := changePortableNetwork(&network, publicIP); err != nil {
		return err
	}
	content, err = json.Marshal(network)
	if err != nil {
		return err
	}
	return os.WriteFile(config, content, 0600)
}

func changePortableNetwork(network *NetworkConfig, host string) error {
	if network.Port == 0 {
		network.Port = 8443
	}
	if network.IdentityOrigin == "" {
		previous := Endpoint{Host: network.IP, Port: network.Port, Type: "https"}
		if err := ValidateEndpoints([]Endpoint{previous}); err != nil {
			return err
		}
		network.IdentityOrigin = previous.Origin()
	}
	if len(network.Endpoints) == 0 {
		network.Endpoints = []Endpoint{{Host: network.IP, Port: network.Port, Type: "https"}}
		network.Revision = 1
		network.ListenHost, network.ListenPort = "0.0.0.0", network.Port
	}
	network.Endpoints[0].Host, network.IP = host, host
	network.Revision++
	if network.Revision > MaxEndpointRevision {
		return errors.New("Исчерпана версия адресов")
	}
	return ValidateEndpoints(network.Endpoints)
}

func validatePortableIP(publicIP string) error {
	if publicIP == "" {
		return nil
	}
	if !ValidEndpointHost(publicIP) {
		return errors.New("Нужен IPv4, IPv6 или домен нового компьютера")
	}
	ip := net.ParseIP(publicIP)
	if ip != nil && (!ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback()) {
		return errors.New("Нужен публичный адрес нового компьютера")
	}
	return nil
}
