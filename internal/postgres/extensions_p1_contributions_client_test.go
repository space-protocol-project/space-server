package postgres

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
)

// Отдельный loopback-стенд для нативного Flutter в CI; не рабочий endpoint.
func TestContributionsNativeClientFixture(t *testing.T) {
	dir := os.Getenv("SPACE_P1_CONTRIBUTIONS_CLIENT")
	if dir == "" {
		t.Skip("Требуется нативный клиент CI")
	}
	f := newChannelFixtureTimeout(t, 30*time.Minute)
	c := f.create(t, "contributions_client")
	f.acl(t, c, roleRule("member", permissions(true, true, false, false)))
	member, _ := f.enroll(t, true)
	host, err := extensions.NewProcessHost(os.Getenv("SPACE_P1_CGROUP"), os.Getenv("SPACE_P1_WORKER"))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	service := contributionService(t, f, host, c.Id)
	managedChange(t, f, service, "enable", 1)
	_, handler := service.NewDeliveryHandler()
	server := httptest.NewServer(handler)
	defer server.Close()
	raw, err := json.Marshal(map[string]string{"url": server.URL, "authorization": outgoingAuthorization(member)})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "connection.tmp"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(dir, "connection.tmp"), filepath.Join(dir, "connection.json")); err != nil {
		t.Fatal(err)
	}
	previous := ""
	for {
		select {
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
			return
		case <-time.After(50 * time.Millisecond):
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
		case "hide":
			f.acl(t, c, roleRule("member", permissions(false, false, false, false)))
		case "restore":
			f.acl(t, c, roleRule("member", permissions(true, true, false, false)))
		case "logout":
			_, err = f.auth.Logout(member, &pb.LogoutRequest{})
		case "stop":
			mutationCounts(t, f, 0, 0, 0, 0)
			return
		default:
			t.Fatal("Неизвестная команда стенда")
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "completed"), raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
}
