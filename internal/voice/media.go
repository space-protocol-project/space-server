package voice

import (
	"context"
	"log"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func validateOffer(raw string) error {
	if len(raw) == 0 || len(raw) > 12*1024 {
		return status.Error(codes.InvalidArgument, "Некорректный размер SDP")
	}
	description := &sdp.SessionDescription{}
	if description.Unmarshal([]byte(raw)) != nil || len(description.MediaDescriptions) != 1 || description.MediaDescriptions[0].MediaName.Media != "audio" {
		return status.Error(codes.InvalidArgument, "Разрешена только одна аудиодорожка")
	}
	return nil
}

func (s *Service) JoinVoiceRoom(ctx context.Context, req *pb.JoinVoiceRoomRequest) (reply *pb.JoinVoiceRoomResponse, err error) {
	if err = validateOffer(req.OfferSdp); err != nil {
		return nil, err
	}
	identity, err := s.identify(ctx, req.ChannelId)
	if err != nil {
		return nil, err
	}
	item, err := s.reserve(ctx, identity, req.ChannelId)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			s.remove(item.id)
		}
	}()
	if err = s.preparePeer(item); err != nil {
		return nil, err
	}
	answer, err := s.answer(ctx, item, req.OfferSdp)
	if err != nil {
		return nil, err
	}
	// Авторизация перепроверяется после установки соединения и перед публикацией.
	identity, err = s.identify(ctx, req.ChannelId)
	if err != nil {
		return nil, err
	}
	item.publish.Store(identity.CanPublish)
	go s.monitor(item)
	return &pb.JoinVoiceRoomResponse{SessionId: item.id, AnswerSdp: answer, State: s.snapshot(req.ChannelId, identity.CanPublish)}, nil
}

func (s *Service) preparePeer(item *mediaSession) error {
	peer, err := s.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "remote", "space-voice")
	if err != nil {
		_ = peer.Close()
		return err
	}
	sender, err := peer.AddTrack(track)
	if err != nil {
		_ = peer.Close()
		return err
	}
	s.mu.Lock()
	if s.sessions[item.id] != item {
		s.mu.Unlock()
		_ = peer.Close()
		return status.Error(codes.Canceled, "Вход отменён")
	}
	item.peer, item.outgoing = peer, track
	s.mu.Unlock()
	go func() {
		buffer := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buffer); err != nil {
				return
			}
		}
	}()
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			go s.remove(item.id)
		}
	})
	peer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { s.receive(item, track) })
	return nil
}

func (s *Service) answer(ctx context.Context, item *mediaSession, offer string) (string, error) {
	if err := item.peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		return "", status.Error(codes.InvalidArgument, "Не удалось согласовать аудио")
	}
	answer, err := item.peer.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	gathered := webrtc.GatheringCompletePromise(item.peer)
	if err = item.peer.SetLocalDescription(answer); err != nil {
		return "", err
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-s.ctx.Done():
		return "", s.ctx.Err()
	case <-timer.C:
		return "", status.Error(codes.DeadlineExceeded, "Не удалось собрать адреса WebRTC")
	case <-gathered:
	}
	return item.peer.LocalDescription().SDP, nil
}

func (s *Service) receive(item *mediaSession, track *webrtc.TrackRemote) {
	if track.Kind() != webrtc.RTPCodecTypeAudio || track.Codec().MimeType != webrtc.MimeTypeOpus || !item.input.CompareAndSwap(false, true) {
		go s.remove(item.id)
		return
	}
	packets := rate.NewLimiter(100, 200)
	bytes := rate.NewLimiter(96*1024, 192*1024)
	for {
		packet, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		if len(packet.Payload) > 1500 || !packets.Allow() || !bytes.AllowN(time.Now(), packet.MarshalSize()) {
			go s.remove(item.id)
			return
		}
		s.forward(item, packet)
	}
}

func (s *Service) forward(source *mediaSession, packet *rtp.Packet) {
	if source.muted.Load() || !source.publish.Load() || time.Now().UnixMilli() >= source.expires.Load() {
		return
	}
	s.mu.Lock()
	if s.sessions[source.id] != source {
		s.mu.Unlock()
		return
	}
	var destinations []*mediaSession
	for _, target := range s.sessions {
		if target != source && target.room == source.room && target.outgoing != nil && !target.deafened.Load() && time.Now().UnixMilli() < target.expires.Load() {
			destinations = append(destinations, target)
		}
	}
	s.mu.Unlock()
	for _, target := range destinations {
		_ = target.outgoing.WriteRTP(packet)
	}
}

func (s *Service) monitor(item *mediaSession) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	defer s.remove(item.id)
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-item.done:
			return
		case <-ticker.C:
		}
		if time.Now().UnixMilli() >= item.expires.Load() {
			return
		}
		s.mu.Lock()
		auth := item.auth
		s.mu.Unlock()
		ctx, cancel := context.WithTimeout(auth, 2*time.Second)
		identity, err := s.identify(ctx, item.room)
		cancel()
		if err != nil || identity.Actor != item.identity.Actor {
			log.Printf("voice authorization ended: %v; device matched: %t", err, identity.Actor == item.identity.Actor)
			return
		}
		item.publish.Store(identity.CanPublish)
		if !identity.CanPublish {
			item.muted.Store(true)
		}
	}
}

func (s *Service) identifyOwner(ctx context.Context, room, id string) (string, error) {
	if len(id) > 64 || len(room) > 64 {
		return "", status.Error(codes.InvalidArgument, "Некорректное подключение")
	}
	// Обновление access token не меняет устройство и не должно мешать выходу.
	identity, identityErr := s.identify(ctx, room)
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.sessions[id]
	if item == nil {
		return "", nil
	}
	if identityErr == nil && item.room == room && item.identity.Actor == identity.Actor {
		return id, nil
	}
	// Токен проверен сетевым interceptor; допуск устройства сверяется дополнительно.
	if item.room != room || item.identity.Actor.PrincipalID != authn.Actor(ctx) || authn.Token(item.auth) != authn.Token(ctx) {
		return "", status.Error(codes.PermissionDenied, "Это чужое подключение")
	}
	return id, nil
}
