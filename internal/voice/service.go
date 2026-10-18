package voice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Identity struct {
	Actor      Actor
	Name       string
	CanPublish bool
}
type IdentityStore interface {
	VoiceIdentity(context.Context, string) (Identity, error)
}

type mediaSession struct {
	id, room                 string
	identity                 Identity
	auth                     context.Context
	peer                     *webrtc.PeerConnection
	outgoing                 *webrtc.TrackLocalStaticRTP
	muted, deafened, publish atomic.Bool
	expires                  atomic.Int64
	input                    atomic.Bool
	done                     chan struct{}
}

type Service struct {
	pb.UnimplementedVoiceServiceServer
	mu       sync.Mutex
	sessions map[string]*mediaSession
	store    IdentityStore
	ctx      context.Context
	api      *webrtc.API
	network  NetworkConfig
}

func NewService(ctx context.Context, store IdentityStore, configuration ...NetworkConfig) (*Service, error) {
	network := NetworkConfig{}
	if len(configuration) > 0 {
		network = configuration[0]
	}
	settings, err := network.settings()
	if err != nil {
		return nil, err
	}
	api, err := NewOpusAPI(settings)
	if err != nil {
		return nil, err
	}
	return &Service{ctx: ctx, store: store, api: api, network: network, sessions: make(map[string]*mediaSession)}, nil
}

func (s *Service) identify(ctx context.Context, room string) (Identity, error) {
	if s.store == nil || len(room) == 0 || len(room) > 64 {
		return Identity{}, status.Error(codes.PermissionDenied, "Комната недоступна")
	}
	return s.store.VoiceIdentity(ctx, room)
}

func (s *Service) reserve(ctx context.Context, identity Identity, room string) (*mediaSession, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, existing := range s.sessions {
		if existing.identity.Actor == identity.Actor {
			return nil, status.Error(codes.AlreadyExists, "Сначала завершите прежнее голосовое подключение")
		}
		if existing.room == room {
			count++
		}
	}
	if count >= 2 || len(s.sessions) >= 128 {
		return nil, status.Error(codes.ResourceExhausted, "Первый срез поддерживает двух участников в комнате")
	}
	item := &mediaSession{id: hex.EncodeToString(random[:]), room: room, identity: identity, auth: context.WithoutCancel(ctx), done: make(chan struct{})}
	item.muted.Store(true)
	item.publish.Store(identity.CanPublish)
	item.expires.Store(time.Now().Add(30 * time.Second).UnixMilli())
	s.sessions[item.id] = item
	return item, nil
}

func (s *Service) remove(id string) {
	s.mu.Lock()
	item := s.sessions[id]
	var peer *webrtc.PeerConnection
	if item != nil {
		peer = item.peer
		item.muted.Store(true)
		item.publish.Store(false)
		item.expires.Store(0)
		delete(s.sessions, id)
		close(item.done)
	}
	s.mu.Unlock()
	if peer != nil {
		_ = peer.Close()
	}
}

func (s *Service) owned(identity Identity, room, id string) (*mediaSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.sessions[id]
	if item == nil || item.room != room || item.identity.Actor != identity.Actor {
		return nil, status.Error(codes.NotFound, "Голосовое подключение завершено")
	}
	return item, nil
}

func (s *Service) snapshot(room string, publish bool) *pb.VoiceRoomState {
	result := &pb.VoiceRoomState{CanPublish: publish, Capacity: 2}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.sessions {
		if item.room != room {
			continue
		}
		result.Participants = append(result.Participants, &pb.VoiceParticipant{SessionId: item.id, PrincipalId: item.identity.Actor.PrincipalID, DeviceId: item.identity.Actor.DeviceID, Nickname: item.identity.Name, Muted: item.muted.Load(), Deafened: item.deafened.Load()})
	}
	sort.Slice(result.Participants, func(i, j int) bool { return result.Participants[i].SessionId < result.Participants[j].SessionId })
	return result
}

func (s *Service) GetVoiceRoom(ctx context.Context, req *pb.GetVoiceRoomRequest) (*pb.GetVoiceRoomResponse, error) {
	identity, err := s.identify(ctx, req.ChannelId)
	if err != nil {
		return nil, err
	}
	return &pb.GetVoiceRoomResponse{State: s.snapshot(req.ChannelId, identity.CanPublish)}, nil
}

func (s *Service) UpdateVoiceRoom(ctx context.Context, req *pb.UpdateVoiceRoomRequest) (*pb.UpdateVoiceRoomResponse, error) {
	identity, err := s.identify(ctx, req.ChannelId)
	if err != nil {
		return nil, err
	}
	item, err := s.owned(identity, req.ChannelId, req.SessionId)
	if err != nil {
		return nil, err
	}
	if !req.Muted && !req.Deafened && !identity.CanPublish {
		return nil, status.Error(codes.PermissionDenied, "Нет права говорить в комнате")
	}
	item.publish.Store(identity.CanPublish)
	item.muted.Store(req.Muted || req.Deafened)
	item.deafened.Store(req.Deafened)
	item.expires.Store(time.Now().Add(30 * time.Second).UnixMilli())
	// Обновлённый access token используется монитором после очередного входа.
	s.mu.Lock()
	item.auth = context.WithoutCancel(ctx)
	s.mu.Unlock()
	return &pb.UpdateVoiceRoomResponse{State: s.snapshot(req.ChannelId, identity.CanPublish)}, nil
}

func (s *Service) LeaveVoiceRoom(ctx context.Context, req *pb.LeaveVoiceRoomRequest) (*pb.LeaveVoiceRoomResponse, error) {
	// Выход не требует прежнего ACL, но чужой допуск не может закрыть соединение.
	identity, err := s.identifyOwner(ctx, req.ChannelId, req.SessionId)
	if err != nil {
		return nil, err
	}
	if identity != "" {
		s.remove(identity)
	}
	return &pb.LeaveVoiceRoomResponse{}, nil
}
