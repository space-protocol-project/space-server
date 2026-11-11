package postgres

import (
	"encoding/json"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/transport"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Только loopback-стенд CI; временные допуски не включаются в артефакты.
func TestBuiltinClientFixture(t *testing.T) {
	dir := os.Getenv("SPACE_P2_CLIENT")
	if dir == "" {
		t.Skip("Нужен клиентский стенд P2")
	}
	f := newChannelFixtureTimeout(t, 15*time.Minute)
	channel := f.create(t, "client_chat")
	forumSection(t, f, "client_forum")
	feedSection(t, f, "client_feed")
	if _, err := f.channels.CreateChannel(f.owner, &pb.CreateChannelRequest{ChannelId: "client_room", Title: "Комната", ViewType: "voice_room"}); err != nil {
		t.Fatal(err)
	}
	member, _ := f.enroll(t, false)
	f.acl(t, channel, roleRule("member", permissions(true, true, true, false)))
	listing, err := f.channels.ListChannels(member, &pb.ListChannelsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	writable := false
	for _, item := range listing.Channels {
		if item.Id == channel.Id {
			writable = item.GetPermissions().GetWrite()
		}
	}
	if !writable {
		t.Fatal("Исходный допуск стенда должен разрешать запись")
	}
	handler := transport.WithConnect(http.NotFoundHandler(), f.conn, f.store)
	var drop atomic.Bool
	var commands atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/space.v1.BuiltinExtensionService/ExecuteBuiltinCommand" {
			handler.ServeHTTP(w, r)
			return
		}
		commands.Add(1)
		if !drop.Swap(false) {
			handler.ServeHTTP(w, r)
			return
		}
		// Обработчик фиксирует запись; клиент получает неопределённый сетевой результат.
		record := httptest.NewRecorder()
		handler.ServeHTTP(record, r)
		http.Error(w, "Ответ потерян после записи", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	raw, err := json.Marshal(map[string]string{"url": server.URL, "authorization": outgoingAuthorization(member)})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "connection.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	previous := ""
	for {
		select {
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
			return
		case <-time.After(25 * time.Millisecond):
		}
		raw, err = os.ReadFile(filepath.Join(dir, "control"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		command := string(raw)
		if command == previous {
			continue
		}
		previous = command
		switch command {
		case "drop":
			drop.Store(true)
		case "hide":
			f.acl(t, channel, roleRule("member", permissions(false, false, false, false)))
		case "restore":
			f.acl(t, channel, roleRule("member", permissions(true, true, true, false)))
		case "readonly":
			f.acl(t, channel, roleRule("member", permissions(true, true, false, false)))
		case "logout":
			_, err = f.auth.Logout(member, &pb.LogoutRequest{})
		case "verify":
			page, listErr := f.contents.ListContent(f.owner, &pb.ListContentRequest{ChannelId: channel.Id})
			if listErr != nil || len(page.Contents) != 1 {
				t.Fatalf("Нарушен единый writer: %v", listErr)
			}
			if commands.Load() != 4 {
				t.Fatalf("Ожидались 2 попытки записи, повтор старого ключа и отказ ACL: %d", commands.Load())
			}
		case "stop":
			return
		default:
			t.Fatal("Неизвестная команда стенда")
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "completed"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
