//go:build linux

package postgres

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Только незашифрованный протокол выделенной БД CI. Производственный транспорт не меняется.
type extensionDatabaseProxy struct {
	mode        string
	armed       atomic.Bool
	hit         chan string
	listener    net.Listener
	mu          sync.Mutex
	closed      bool
	connections []net.Conn
	wg          sync.WaitGroup
}

func newExtensionDatabaseProxy(t *testing.T, database, mode string) (*extensionDatabaseProxy, string) {
	t.Helper()
	u, err := url.Parse(database)
	if err != nil || u.Query().Get("sslmode") != "disable" {
		t.Fatal("Нужна тестовая БД без TLS", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &extensionDatabaseProxy{mode: mode, hit: make(chan string, 1), listener: listener}
	target := u.Host
	u.Host = listener.Addr().String()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			if p.closed {
				p.mu.Unlock()
				client.Close()
				return
			}
			p.connections = append(p.connections, client)
			p.mu.Unlock()
			p.wg.Add(1)
			go func() { defer p.wg.Done(); p.serve(client, target) }()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		p.mu.Lock()
		p.closed = true
		for _, c := range p.connections {
			c.Close()
		}
		p.mu.Unlock()
		p.wg.Wait()
	})
	return p, u.String()
}

func (p *extensionDatabaseProxy) serve(client net.Conn, target string) {
	defer client.Close()
	server, err := net.DialTimeout("tcp", target, time.Second)
	if err != nil {
		return
	}
	defer server.Close()
	// StartupMessage не имеет байта типа; далее обе стороны обмениваются кадрами.
	startup, err := extensionDatabaseFrame(client, false)
	if err != nil {
		return
	}
	if _, err = server.Write(startup); err != nil {
		return
	}
	var selected atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		defer client.Close()
		p.forward(client, server, true, &selected)
	}()
	p.forward(server, client, false, &selected)
	server.Close()
	client.Close()
	<-done
}

func extensionDatabaseFrame(r io.Reader, typed bool) ([]byte, error) {
	offset := 0
	if typed {
		offset = 1
	}
	header := make([]byte, 4+offset)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint32(header[offset:]))
	if size < 4 || size > 8<<20 {
		return nil, fmt.Errorf("invalid test frame size: %d", size)
	}
	frame := make([]byte, size+offset)
	copy(frame, header)
	_, err := io.ReadFull(r, frame[len(header):])
	return frame, err
}

func (p *extensionDatabaseProxy) forward(source, target net.Conn, frontend bool, selected *atomic.Bool) {
	for {
		frame, err := extensionDatabaseFrame(source, true)
		if err != nil {
			return
		}
		if p.drop(frame, frontend, selected) {
			return
		}
		if _, err = target.Write(frame); err != nil {
			return
		}
	}
}

func (p *extensionDatabaseProxy) drop(frame []byte, frontend bool, selected *atomic.Bool) bool {
	payload := strings.TrimSuffix(string(frame[5:]), "\x00")
	if frontend && frame[0] == 'Q' && strings.EqualFold(strings.TrimSpace(payload), "commit") && p.armed.CompareAndSwap(true, false) {
		selected.Store(true)
		if p.mode == "before_commit" {
			p.hit <- "COMMIT not forwarded"
			return true
		}
	}
	if !frontend && selected.Load() && frame[0] == 'C' && payload == "COMMIT" {
		p.hit <- "CommandComplete COMMIT withheld"
		return true
	}
	return false
}
