package voice

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type voicePolicy struct {
	mu      sync.Mutex
	blocked map[string]bool
	reader  map[string]bool
}

func (p *voicePolicy) Authenticate(_ context.Context, token string) (string, error) {
	if token == "" {
		return "", ErrDenied
	}
	return "principal-" + token, nil
}
func (p *voicePolicy) VoiceIdentity(ctx context.Context, room string) (Identity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	token := authn.Token(ctx)
	if token == "" || p.blocked[token] || room != "room" {
		return Identity{}, status.Error(codes.PermissionDenied, "Комната недоступна")
	}
	return Identity{Actor: Actor{"server", authn.Actor(ctx), "device-" + token}, Name: token, CanPublish: !p.reader[token]}, nil
}
func (p *voicePolicy) auth(t *testing.T, ctx context.Context, token string) context.Context {
	t.Helper()
	result, err := authn.AuthorizeCall(metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)), p, "/space.v1.VoiceService/JoinVoiceRoom")
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func servicePeer(t *testing.T, ctx context.Context, service *Service, peer *webrtc.PeerConnection) *pb.JoinVoiceRoomResponse {
	t.Helper()
	offer, err := peer.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather(t, ctx, peer, offer)
	reply, err := service.JoinVoiceRoom(ctx, &pb.JoinVoiceRoomRequest{ChannelId: "room", OfferSdp: peer.LocalDescription().SDP})
	if err != nil {
		t.Fatal(err)
	}
	if err = peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: reply.AnswerSdp}); err != nil {
		t.Fatal(err)
	}
	return reply
}

func receiveMarkers(peer *webrtc.PeerConnection) <-chan byte {
	result := make(chan byte, 128)
	peer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			if len(packet.Payload) == 3 {
				select {
				case result <- packet.Payload[2]:
				default:
				}
			}
		}
	})
	return result
}

func sendMarker(t *testing.T, ctx context.Context, track *webrtc.TrackLocalStaticRTP, received <-chan byte, marker byte, want bool) {
	t.Helper()
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	if want {
		timer.Stop()
		timer.Reset(3 * time.Second)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var sequence uint16
	for {
		select {
		case value := <-received:
			if value == marker {
				if !want {
					t.Fatal("Звук прошёл при запрете")
				}
				return
			}
		case <-ticker.C:
			sequence++
			if err := track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: sequence, Timestamp: uint32(sequence) * 960, SSRC: 27}, Payload: []byte{0xf8, 0xff, marker}}); err != nil {
				t.Fatal(err)
			}
		case <-timer.C:
			if want {
				t.Fatal("Нет аудио через комнату")
			}
			return
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestTwoClientsVoiceControlsAndRevocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	policy := &voicePolicy{blocked: map[string]bool{}, reader: map[string]bool{}}
	service, err := NewService(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	aCtx, bCtx := policy.auth(t, ctx, "A"), policy.auth(t, ctx, "B")
	a, b := loopbackPeer(t), loopbackPeer(t)
	aTrack, bTrack := addOpus(t, a, "A"), addOpus(t, b, "B")
	toA, toB := receiveMarkers(a), receiveMarkers(b)
	aJoin, bJoin := servicePeer(t, aCtx, service, a), servicePeer(t, bCtx, service, b)
	if len(bJoin.State.Participants) != 2 {
		t.Fatal("нет двух участников")
	}
	for _, person := range bJoin.State.Participants {
		if !person.Muted {
			t.Fatal("микрофон включён при входе")
		}
	}
	set := func(auth context.Context, id string, muted, deafened bool) {
		t.Helper()
		if _, err := service.UpdateVoiceRoom(auth, &pb.UpdateVoiceRoomRequest{ChannelId: "room", SessionId: id, Muted: muted, Deafened: deafened}); err != nil {
			t.Fatal(err)
		}
	}
	sendMarker(t, ctx, aTrack, toB, 1, false)
	set(aCtx, aJoin.SessionId, false, false)
	sendMarker(t, ctx, aTrack, toB, 2, true)
	set(bCtx, bJoin.SessionId, false, false)
	sendMarker(t, ctx, bTrack, toA, 3, true)
	set(bCtx, bJoin.SessionId, false, true)
	sendMarker(t, ctx, aTrack, toB, 4, false)
	sendMarker(t, ctx, bTrack, toA, 5, false)
	set(bCtx, bJoin.SessionId, true, false)
	sendMarker(t, ctx, aTrack, toB, 6, true)
	set(aCtx, aJoin.SessionId, true, false)
	sendMarker(t, ctx, aTrack, toB, 7, false)
	if _, err := service.LeaveVoiceRoom(aCtx, &pb.LeaveVoiceRoomRequest{ChannelId: "room", SessionId: bJoin.SessionId}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("чужое подключение закрыто", err)
	}
	policy.mu.Lock()
	policy.blocked["B"] = true
	policy.mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for {
		frame, err := service.GetVoiceRoom(aCtx, &pb.GetVoiceRoomRequest{ChannelId: "room"})
		if err != nil {
			t.Fatal(err)
		}
		if len(frame.State.Participants) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("отзыв не закрыл медиа")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := service.LeaveVoiceRoom(aCtx, &pb.LeaveVoiceRoomRequest{ChannelId: "room", SessionId: aJoin.SessionId}); err != nil {
		t.Fatal(err)
	}
	frame, _ := service.GetVoiceRoom(aCtx, &pb.GetVoiceRoomRequest{ChannelId: "room"})
	if len(frame.State.Participants) != 0 {
		t.Fatal("выход не очистил комнату")
	}
}

func TestVoiceRejectsVideoAndReadOnlyPublication(t *testing.T) {
	if validateOffer("m=video 9 UDP/TLS/RTP/SAVPF 96\r\n") == nil {
		t.Fatal("видео разрешено")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	policy := &voicePolicy{blocked: map[string]bool{}, reader: map[string]bool{"reader": true}}
	service, err := NewService(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	auth := policy.auth(t, ctx, "reader")
	peer := loopbackPeer(t)
	addOpus(t, peer, "read-only")
	reply := servicePeer(t, auth, service, peer)
	if reply.State.CanPublish {
		t.Fatal("читатель получил право говорить")
	}
	_, err = service.UpdateVoiceRoom(auth, &pb.UpdateVoiceRoomRequest{ChannelId: "room", SessionId: reply.SessionId})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	if _, err = service.GetVoiceRoom(context.Background(), &pb.GetVoiceRoomRequest{ChannelId: "room"}); !errors.Is(err, ErrDenied) && status.Code(err) != codes.PermissionDenied {
		t.Fatal("анонимное чтение", err)
	}
}
