package postgres

import (
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestIconValidation(t *testing.T) {
	for _, svg := range []string{`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M0 0L24 24"/></svg>`} {
		if err := validateServerIcon(&pb.ServerIcon{MediaType: "image/svg+xml", Data: []byte(svg)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, svg := range []string{`<svg><script>alert(1)</script></svg>`, `<svg><image href="https://example.org/a.png"/></svg>`, `<svg onload="alert(1)"></svg>`, `<!DOCTYPE svg><svg/>`, `<svg><path fill="url(https://example.org)"/></svg>`} {
		if err := validateServerIcon(&pb.ServerIcon{MediaType: "image/svg+xml", Data: []byte(svg)}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("Опасный SVG принят: %s", svg)
		}
	}
	if err := validateServerIcon(&pb.ServerIcon{MediaType: "image/png", Data: make([]byte, 8193)}); status.Code(err) != codes.InvalidArgument {
		t.Fatal("Размер принят")
	}
}
func TestServerIconSharedAndPreserved(t *testing.T) {
	f := newChannelFixture(t)
	member, _ := f.enroll(t, false)
	settings, err := f.admin.GetSettings(f.owner, &pb.GetSettingsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	watchContext, cancelWatch := context.WithCancel(f.owner)
	defer cancelWatch()
	watch, err := f.channels.WatchChannels(watchContext, &pb.WatchChannelsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := watch.Recv()
	if err != nil || first.Icon.Preset != "orbit" {
		t.Fatalf("Иконка по умолчанию: %v %v", first, err)
	}
	req := &pb.UpdateSettingsRequest{Title: settings.Settings.Title, ChatTitle: settings.Settings.ChatTitle, ChatEnabled: settings.Settings.ChatEnabled, RegistrationPolicy: settings.Settings.RegistrationPolicy, ExpectedRevision: settings.Settings.Revision, Icon: &pb.ServerIcon{Preset: "star"}}
	if _, err = f.admin.UpdateSettings(member, req); status.Code(err) != codes.PermissionDenied {
		t.Fatal("Участник изменил иконку", err)
	}
	saved, err := f.admin.UpdateSettings(f.owner, req)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := watch.Recv()
	if err != nil || frame.Icon.Preset != "star" {
		t.Fatalf("Изменение не доставлено: %v %v", frame, err)
	}
	for _, ctx := range []struct {
		name   string
		public bool
	}{{"public", true}, {"member", false}} {
		context := member
		if ctx.public {
			context = f.ctx
		}
		manifest, err := f.channels.GetManifest(context, &pb.GetManifestRequest{})
		if err != nil || !proto.Equal(manifest.Icon, req.Icon) {
			t.Fatalf("Иконка %s: %v %v", ctx.name, manifest, err)
		}
	}
	req.Icon = nil
	req.ExpectedRevision = saved.Settings.Revision
	legacy, err := f.admin.UpdateSettings(f.owner, req)
	if err != nil || legacy.Settings.Icon.Preset != "star" {
		t.Fatalf("Старый клиент сбросил иконку: %v %v", legacy, err)
	}
	req.Icon = &pb.ServerIcon{MediaType: "image/svg+xml", Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0L24 24"/></svg>`)}
	req.ExpectedRevision = legacy.Settings.Revision
	if _, err = f.admin.UpdateSettings(f.owner, req); err != nil {
		t.Fatal(err)
	}
	cancelWatch()
	f.store.Close()
	reopened, _, err := openManual(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	manifest, err := reopened.GetManifest(f.ctx, &pb.GetManifestRequest{})
	if err != nil || !proto.Equal(manifest.Icon, req.Icon) {
		t.Fatalf("Иконка потерялась после перезапуска: %v %v", manifest, err)
	}
}
