// Package home запускает отдельную локальную PostgreSQL для домашнего сервера.
package home

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

func executable(root, name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(root, "postgres", "bin", name)
}

func command(root, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(executable(root, name), args...)
	lib := filepath.Join(root, "postgres", "lib")
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+lib, "DYLD_LIBRARY_PATH="+lib)
	hideWindow(cmd)
	return cmd
}

// StartDatabase не изменяет системную PostgreSQL и никогда не слушает внешний адрес.
func StartDatabase(root, data string) (string, func(), error) {
	if err := os.MkdirAll(data, 0700); err != nil {
		return "", nil, err
	}
	passwordPath := filepath.Join(data, "database-password")
	password, err := os.ReadFile(passwordPath)
	if os.IsNotExist(err) {
		bytes := make([]byte, 32)
		if _, err = rand.Read(bytes); err != nil {
			return "", nil, err
		}
		password = []byte(base64.RawURLEncoding.EncodeToString(bytes))
		err = os.WriteFile(passwordPath, password, 0600)
	}
	if err != nil || len(password) < 32 {
		return "", nil, fmt.Errorf("Не удалось прочитать пароль домашней базы")
	}
	database := filepath.Join(data, "postgres")
	if _, err = os.Stat(filepath.Join(database, "PG_VERSION")); os.IsNotExist(err) {
		staging, err := os.MkdirTemp(data, "postgres-init-")
		if err != nil {
			return "", nil, err
		}
		defer os.RemoveAll(staging)
		cmd := command(root, "initdb", "-D", staging, "-U", "space", "--auth=scram-sha-256", "--encoding=UTF8", "--locale=C", "--pwfile="+passwordPath)
		if output, err := cmd.CombinedOutput(); err != nil {
			return "", nil, fmt.Errorf("initdb: %w: %s", err, output)
		}
		if err := os.Rename(staging, database); err != nil {
			return "", nil, err
		}
	} else if err != nil {
		return "", nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	options := fmt.Sprintf("-h 127.0.0.1 -p %d -c unix_socket_directories='' -c logging_collector=on -c log_rotation_size=10MB -c log_truncate_on_rotation=on", port)
	cmd := command(root, "pg_ctl", "-D", database, "-l", filepath.Join(data, "postgres-start.log"), "-o", options, "-w", "-t", "30", "start")
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("pg_ctl: %w: %s", err, output)
	}
	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		base := command(root, "pg_ctl", "-D", database, "-m", "fast", "-w", "-t", "15", "stop")
		cmd := exec.CommandContext(ctx, base.Path, base.Args[1:]...)
		cmd.Env = base.Env
		hideWindow(cmd)
		_ = cmd.Run()
	}
	address := &url.URL{Scheme: "postgres", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Path: "/postgres", User: url.UserPassword("space", string(password)), RawQuery: "sslmode=disable"}
	return address.String(), stop, nil
}
