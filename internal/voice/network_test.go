package voice

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/turn/v5"
	"github.com/pion/webrtc/v4"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/voice/turnfixture"
)

func TestNetworkEnvironmentSecretFileAndFailClosed(t *testing.T) {
	for _, name := range []string{"SPACE_VOICE_STUN_URLS", "SPACE_VOICE_TURN_URLS", "SPACE_VOICE_TURN_SECRET", "SPACE_VOICE_TURN_SECRET_FILE", "SPACE_VOICE_PUBLIC_IP", "SPACE_VOICE_UDP_MIN", "SPACE_VOICE_UDP_MAX", "SPACE_VOICE_RELAY_ONLY"} {
		t.Setenv(name, "")
	}
	path := filepath.Join(t.TempDir(), "turn-secret")
	if err := os.WriteFile(path, []byte(strings.Repeat("s", 32)+"\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPACE_VOICE_TURN_URLS", "turn:example.org:3478?transport=tcp")
	t.Setenv("SPACE_VOICE_TURN_SECRET_FILE", path)
	t.Setenv("SPACE_VOICE_RELAY_ONLY", "true")
	config, err := NetworkFromEnvironment()
	if err != nil || !config.RelayOnly || len(config.TURNSecret) != 32 {
		t.Fatal(config.RelayOnly, err)
	}
	t.Setenv("SPACE_VOICE_TURN_SECRET", "must-not-appear-in-errors")
	_, err = NetworkFromEnvironment()
	if err == nil || strings.Contains(err.Error(), "must-not-appear-in-errors") {
		t.Fatal("ambiguous secret source or leaked secret", err)
	}
	t.Setenv("SPACE_VOICE_TURN_SECRET", "")
	t.Setenv("SPACE_VOICE_RELAY_ONLY", "tru")
	if _, err = NetworkFromEnvironment(); err == nil {
		t.Fatal("silently accepted invalid relay-only setting")
	}
}

func TestTURNConfigurationAuthorizationAndCredentials(t *testing.T) {
	config := NetworkConfig{TURNURLs: []string{"turn:relay.example.org:3478?transport=tcp"}, TURNSecret: []byte(strings.Repeat("s", 32)), RelayOnly: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	policy := &voicePolicy{blocked: map[string]bool{}, reader: map[string]bool{}}
	service, err := NewService(ctx, policy, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.GetVoiceConfiguration(ctx, &pb.GetVoiceConfigurationRequest{ChannelId: "room"}); err == nil {
		t.Fatal("anonymous TURN access")
	}
	auth := policy.auth(t, ctx, "A")
	issued, err := service.GetVoiceConfiguration(auth, &pb.GetVoiceConfigurationRequest{ChannelId: "room"})
	if err != nil {
		t.Fatal(err)
	}
	entry := issued.IceServers[0]
	handler := turn.LongTermTURNRESTAuthHandler(string(config.TURNSecret), nil)
	_, key, ok := handler(&turn.RequestAttributes{Username: entry.Username, Realm: "test"})
	if !ok || string(key) != string(turn.GenerateAuthKey(entry.Username, "test", entry.Credential)) {
		t.Fatal("incompatible TURN REST credentials")
	}
	if strings.Contains(entry.Username, "principal") || strings.Contains(entry.Credential, string(config.TURNSecret)) {
		t.Fatal("identity or shared secret leaked")
	}
	second, _ := config.credentials()
	if second.IceServers[0].Username == entry.Username {
		t.Fatal("credentials reused")
	}
	policy.mu.Lock()
	policy.blocked["A"] = true
	policy.mu.Unlock()
	if _, err = service.GetVoiceConfiguration(auth, &pb.GetVoiceConfigurationRequest{ChannelId: "room"}); err == nil {
		t.Fatal("revoked TURN access")
	}
}

func TestNetworkConfigurationRejectsIncompleteSettings(t *testing.T) {
	for _, config := range []NetworkConfig{
		{RelayOnly: true}, {TURNURLs: []string{"turn:example.org"}},
		{STUNURLs: []string{"https://example.org"}}, {STUNURLs: []string{"turn:example.org"}},
		{PublicIP: "not-an-ip"}, {UDPMin: 40000}, {UDPMin: 50000, UDPMax: 40000},
	} {
		if config.validate() == nil {
			t.Fatalf("accepted invalid configuration: %+v", config)
		}
	}
}

func relayPeer(t *testing.T, config NetworkConfig) *webrtc.PeerConnection {
	t.Helper()
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
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

func TestTwoClientsForcedThroughTURN(t *testing.T) {
	for _, transport := range []string{"udp", "tcp"} {
		t.Run(transport, func(t *testing.T) {
			fixture, err := turnfixture.Start()
			if err != nil {
				t.Fatal(err)
			}
			defer fixture.Close()
			url := fixture.UDP
			if transport == "tcp" {
				url = fixture.TCP
			}
			config := NetworkConfig{TURNURLs: []string{url}, TURNSecret: []byte(fixture.Secret), RelayOnly: true}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			policy := &voicePolicy{blocked: map[string]bool{}, reader: map[string]bool{}}
			service, err := NewService(ctx, policy, config)
			if err != nil {
				t.Fatal(err)
			}
			a, b := relayPeer(t, config), relayPeer(t, config)
			aTrack, bTrack := addOpus(t, a, "A"), addOpus(t, b, "B")
			toA, toB := receiveMarkers(a), receiveMarkers(b)
			aCtx, bCtx := policy.auth(t, ctx, "A"), policy.auth(t, ctx, "B")
			aJoin, bJoin := servicePeer(t, aCtx, service, a), servicePeer(t, bCtx, service, b)
			for _, pair := range []struct {
				ctx context.Context
				id  string
			}{{aCtx, aJoin.SessionId}, {bCtx, bJoin.SessionId}} {
				if _, err = service.UpdateVoiceRoom(pair.ctx, &pb.UpdateVoiceRoomRequest{ChannelId: "room", SessionId: pair.id}); err != nil {
					t.Fatal(err)
				}
			}
			sendMarker(t, ctx, aTrack, toB, 71, true)
			sendMarker(t, ctx, bTrack, toA, 72, true)
			for _, sdp := range []string{a.LocalDescription().SDP, b.LocalDescription().SDP, aJoin.AnswerSdp, bJoin.AnswerSdp} {
				if !strings.Contains(sdp, " typ relay") || strings.Contains(sdp, " typ host") {
					t.Fatal("direct candidate in relay-only test")
				}
			}
			if fixture.Allocations.Load() < 4 {
				t.Fatal("both clients and runtime did not allocate TURN relays")
			}
		})
	}
}
