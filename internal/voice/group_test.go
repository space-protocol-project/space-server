package voice

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/voice/turnfixture"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type groupPacket struct {
	listener int
	track    string
	marker   byte
}

func observeGroup(peer *webrtc.PeerConnection, listener int, packets chan<- groupPacket) {
	peer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			if len(packet.Payload) != 3 {
				continue
			}
			select {
			case packets <- groupPacket{listener, track.ID(), packet.Payload[2]}:
			default:
			}
		}
	})
}

func publishGroup(ctx context.Context, track *webrtc.TrackLocalStaticRTP, marker byte) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var sequence uint16
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sequence++
			_ = track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: uint32(sequence) * 960, SSRC: 27}, Payload: []byte{0xf8, 0xff, marker}})
		}
	}
}

func TestEightSimultaneousSpeakersAndSlotReuse(t *testing.T) {
	for _, transport := range []string{"direct", "udp", "tcp"} {
		t.Run(transport, func(t *testing.T) { checkEightSpeakers(t, transport) })
	}
}

func checkEightSpeakers(t *testing.T, transport string) { checkGroupSpeakers(t, transport, 8) }

func TestThirtyTwoSimultaneousSpeakersAndSlotReuse(t *testing.T) {
	checkGroupSpeakers(t, "direct", 32)
}

func checkGroupSpeakers(t *testing.T, transport string, roomCapacity int) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	policy := &voicePolicy{capacity: roomCapacity, blocked: map[string]bool{}, reader: map[string]bool{}}
	configuration := NetworkConfig{}
	factory := func() *webrtc.PeerConnection { return loopbackPeer(t) }
	if transport != "direct" {
		fixture, err := turnfixture.Start()
		if err != nil {
			t.Fatal(err)
		}
		defer fixture.Close()
		url := fixture.UDP
		if transport == "tcp" {
			url = fixture.TCP
		}
		configuration = NetworkConfig{TURNURLs: []string{url}, TURNSecret: []byte(fixture.Secret), RelayOnly: true}
		factory = func() *webrtc.PeerConnection { return relayPeer(t, configuration) }
	}
	service, err := NewService(ctx, policy, configuration)
	if err != nil {
		t.Fatal(err)
	}
	packets := make(chan groupPacket, 4096)
	identities := make([]context.Context, roomCapacity)
	joins := make([]*pb.JoinVoiceRoomResponse, roomCapacity)
	tracks := make([]*webrtc.TrackLocalStaticRTP, roomCapacity)
	for i := 0; i < roomCapacity; i++ {
		identities[i] = policy.auth(t, ctx, fmt.Sprint(i))
		peer := factory()
		tracks[i] = addOpus(t, peer, fmt.Sprint(i))
		observeGroup(peer, i, packets)
		joins[i] = servicePeer(t, identities[i], service, peer, roomCapacity)
		if joins[i].State.Capacity != int32(roomCapacity) {
			t.Fatal("Неверная вместимость")
		}
		_, err = service.UpdateVoiceRoom(identities[i], &pb.UpdateVoiceRoomRequest{ChannelId: "room", SessionId: joins[i].SessionId})
		if err != nil {
			t.Fatal(err)
		}
	}
	outsider := policy.auth(t, ctx, "extra")
	identity, _ := service.identify(outsider, "room")
	if _, err = service.reserve(outsider, identity, "room"); status.Code(err) != codes.ResourceExhausted {
		t.Fatal("Девятый участник принят", err)
	}
	for i, track := range tracks {
		go publishGroup(ctx, track, byte(i+1))
	}
	seen := make(map[int]map[string]byte)
	for {
		select {
		case packet := <-packets:
			if packet.marker == byte(packet.listener+1) {
				t.Fatal("Эхо собственного голоса")
			}
			if seen[packet.listener] == nil {
				seen[packet.listener] = make(map[string]byte)
			}
			previous := seen[packet.listener][packet.track]
			if previous != 0 && previous != packet.marker {
				t.Fatal("Разные голоса смешаны в одной RTP дорожке")
			}
			seen[packet.listener][packet.track] = packet.marker
		case <-ctx.Done():
			t.Fatal("Не все 56 направлений доставлены", seen)
		}
		complete := len(seen) == roomCapacity
		for _, sources := range seen {
			complete = complete && len(sources) == roomCapacity-1
		}
		if complete {
			break
		}
	}
	checkGroupMute(t, ctx, service, identities, joins, packets)
	// У оставшихся клиентов нет нового SDP: повторно используется ранее согласованная дорожка.
	if _, err = service.LeaveVoiceRoom(identities[7], &pb.LeaveVoiceRoomRequest{ChannelId: "room", SessionId: joins[7].SessionId}); err != nil {
		t.Fatal(err)
	}
	replacement := factory()
	replacementTrack := addOpus(t, replacement, "replacement")
	observeGroup(replacement, 7, packets)
	joined := servicePeer(t, outsider, service, replacement, roomCapacity)
	if _, err = service.UpdateVoiceRoom(outsider, &pb.UpdateVoiceRoomRequest{ChannelId: "room", SessionId: joined.SessionId}); err != nil {
		t.Fatal(err)
	}
	go publishGroup(ctx, replacementTrack, 255)
	received := make(map[int]bool)
	for len(received) < roomCapacity-1 {
		select {
		case packet := <-packets:
			if packet.marker == 255 {
				if packet.listener == 7 {
					t.Fatal("Эхо заменившего участника")
				}
				if seen[packet.listener][packet.track] != 8 {
					t.Fatal("Повторно использована чужая дорожка")
				}
				received[packet.listener] = true
			}
		case <-ctx.Done():
			t.Fatal("Новый участник не слышен после повторного использования места", received)
		}
	}
}

func checkGroupMute(t *testing.T, ctx context.Context, service *Service, identities []context.Context, joins []*pb.JoinVoiceRoomResponse, packets <-chan groupPacket) {
	t.Helper()
	set := func(index int, muted, deafened bool) {
		t.Helper()
		_, err := service.UpdateVoiceRoom(identities[index], &pb.UpdateVoiceRoomRequest{ChannelId: "room", SessionId: joins[index].SessionId, Muted: muted, Deafened: deafened})
		if err != nil {
			t.Fatal(err)
		}
	}
	set(0, true, false)
	set(1, false, true)
	flush := time.NewTimer(200 * time.Millisecond)
	flushing := true
	for flushing {
		select {
		case <-packets:
		case <-flush.C:
			flushing = false
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	count := 0
	for {
		select {
		case packet := <-packets:
			if packet.listener == 1 || packet.marker <= 2 {
				t.Fatal("mute/deafen не остановил нужный поток", packet)
			}
			count++
		case <-timer.C:
			if count == 0 {
				t.Fatal("Остальные участники перестали говорить")
			}
			set(0, false, false)
			set(1, false, false)
			return
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
