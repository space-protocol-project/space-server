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
	Capacity   int
}
type IdentityStore interface {
	VoiceIdentity(context.Context, string) (Identity, error)
}

const roomCapacity = 8
const MaxRoomCapacity = 32

// Capacity сохраняет прежний лимит для хранилищ без настройки.
func Capacity(value int) int {
	if value == 0 {
		return roomCapacity
	}
	return value
}

type mediaSession struct {
	slot                     int
	id, room                 string
	identity                 Identity
	auth                     context.Context
	peer                     *webrtc.PeerConnection
	outgoing                 []*audioOutput
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

func (s *Service) reserve(ctx context.Context, identity Identity, room string, outputCounts ...int) (*mediaSession, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	outputs := MaxRoomCapacity - 1
	if len(outputCounts) > 0 {
		outputs = outputCounts[0]
	}
	slot, err := s.reserveSlot(identity, room, outputs)
	if err != nil {
		return nil, err
	}
	item := &mediaSession{slot: slot, id: hex.EncodeToString(random[:]), room: room, identity: identity, auth: context.WithoutCancel(ctx), done: make(chan struct{})}
	item.muted.Store(true)
	item.publish.Store(identity.CanPublish)
	item.expires.Store(time.Now().Add(30 * time.Second).UnixMilli())
	s.sessions[item.id] = item
	return item, nil
}

// Вызывается под s.mu: выбор места и резервирование должны быть атомарными.
func (s *Service) occupiedSlots(identity Identity, room string) ([MaxRoomCapacity]bool, int, error) {
	var occupied [MaxRoomCapacity]bool
	count := 0
	for _, existing := range s.sessions {
		if existing.identity.Actor == identity.Actor {
			return occupied, count, status.Error(codes.AlreadyExists, "Сначала завершите прежнее голосовое подключение")
		}
		if existing.room == room {
			occupied[existing.slot] = true
			count++
		}
	}
	return occupied, count, nil
}

func (s *Service) reserveSlot(identity Identity, room string, outputs int) (int, error) {
	occupied, count, err := s.occupiedSlots(identity, room)
	if err != nil {
		return 0, err
	}
	if count >= Capacity(identity.Capacity) || len(s.sessions) >= 128 {
		return 0, status.Error(codes.ResourceExhausted, "Достигнут лимит участников комнаты или голосовых подключений сервера")
	}
	slot := 0
	for occupied[slot] {
		slot++
	}
	if slot > outputs {
		return 0, status.Error(codes.FailedPrecondition, "Обновите приложение для входа в большую голосовую комнату")
	}
	return slot, s.checkReceiverSlots(room, slot, outputs)
}

func (s *Service) checkReceiverSlots(room string, slot, outputs int) error {
	for _, existing := range s.sessions {
		if existing.room != room {
			continue
		}
		if existing.slot > outputs {
			return status.Error(codes.FailedPrecondition, "Обновите приложение для входа в большую голосовую комнату")
		}
		if existing.outgoing != nil && slot > len(existing.outgoing) {
			return status.Error(codes.FailedPrecondition, "Дождитесь переподключения участников после изменения лимита")
		}
	}
	return nil
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

func (s *Service) snapshot(room string, identity Identity) *pb.VoiceRoomState {
	result := &pb.VoiceRoomState{CanPublish: identity.CanPublish, Capacity: int32(Capacity(identity.Capacity))}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.sessions {
		if item.room != room {
			continue
		}
		slot := uint32(item.slot)
		result.Participants = append(result.Participants, &pb.VoiceParticipant{AudioSlot: &slot, SessionId: item.id, PrincipalId: item.identity.Actor.PrincipalID, DeviceId: item.identity.Actor.DeviceID, Nickname: item.identity.Name, Muted: item.muted.Load(), Deafened: item.deafened.Load()})
	}
	sort.Slice(result.Participants, func(i, j int) bool { return result.Participants[i].SessionId < result.Participants[j].SessionId })
	return result
}

func (s *Service) GetVoiceRoom(ctx context.Context, req *pb.GetVoiceRoomRequest) (*pb.GetVoiceRoomResponse, error) {
	identity, err := s.identify(ctx, req.ChannelId)
	if err != nil {
		return nil, err
	}
	return &pb.GetVoiceRoomResponse{State: s.snapshot(req.ChannelId, identity)}, nil
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
	return &pb.UpdateVoiceRoomResponse{State: s.snapshot(req.ChannelId, identity)}, nil
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
