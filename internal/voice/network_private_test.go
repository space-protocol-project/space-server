package voice

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/voice/turnfixture"
)

func privateTestAddress(t *testing.T) string {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range interfaces {
		if item.Flags&net.FlagUp == 0 || item.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := item.Addrs()
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range addresses {
			ip, _, err := net.ParseCIDR(raw.String())
			if err == nil && ip.To4() != nil && ip.IsPrivate() {
				return ip.String()
			}
		}
	}
	t.Fatal("Нет частного IPv4 интерфейса CI")
	return ""
}

func defaultRelayPeer(t *testing.T, config NetworkConfig) *webrtc.PeerConnection {
	t.Helper()
	settings, err := config.settings()
	if err != nil {
		t.Fatal(err)
	}
	api, err := NewOpusAPI(settings)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := config.peerConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	peer, err := api.NewPeerConnection(configuration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	return peer
}

func TestPrivateInterfaceTURN(t *testing.T) {
	if os.Getenv("SPACE_TURN_PRIVATE_CHECK") != "1" {
		t.Skip("Отдельный сетевой стенд CI")
	}
	fixture, err := turnfixture.StartOn(privateTestAddress(t))
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	config := NetworkConfig{TURNURLs: []string{fixture.TCP}, TURNSecret: []byte(fixture.Secret), RelayOnly: true}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	policy := &voicePolicy{blocked: map[string]bool{}, reader: map[string]bool{}}
	service, err := NewService(ctx, policy, config)
	if err != nil {
		t.Fatal(err)
	}
	a, b := defaultRelayPeer(t, config), defaultRelayPeer(t, config)
	aTrack, bTrack := addOpus(t, a, "A"), addOpus(t, b, "B")
	toA, toB := receiveMarkers(a), receiveMarkers(b)
	aCtx, bCtx := policy.auth(t, ctx, "A"), policy.auth(t, ctx, "B")
	started := time.Now()
	aJoin, bJoin := servicePeer(t, aCtx, service, a), servicePeer(t, bCtx, service, b)
	t.Logf("Два offer/answer: %s", time.Since(started))
	for _, pair := range []struct {
		ctx context.Context
		id  string
	}{{aCtx, aJoin.SessionId}, {bCtx, bJoin.SessionId}} {
		if _, err := service.UpdateVoiceRoom(pair.ctx, &pb.UpdateVoiceRoomRequest{ChannelId: "room", SessionId: pair.id}); err != nil {
			t.Fatal(err)
		}
	}
	sendMarker(t, ctx, aTrack, toB, 71, true)
	sendMarker(t, ctx, bTrack, toA, 72, true)
	if fixture.Allocations.Load() < 4 {
		t.Fatal("Клиенты и сервер не создали четыре relay")
	}
}
