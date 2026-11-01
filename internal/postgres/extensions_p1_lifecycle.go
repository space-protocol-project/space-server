package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/lifecycle"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"github.com/space-protocol-project/space-server/internal/extensionp1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

type extensionLifecycleInstallation struct {
	coordinator *lifecycle.Coordinator
	gate        chan struct{}
	channels    []string
	failed      atomic.Bool
}
type extensionLifecycle struct {
	store         *Store
	installations map[string]*extensionLifecycleInstallation
	instances     map[string]string
	channels      map[string]string
}

// ExtensionP1ManagedService — один владелец жизненного цикла в памяти.
// Публичный реестр и несколько управляющих процессов этим API не вводятся.
type ExtensionP1ManagedService struct {
	pb.UnimplementedExtensionP1ServiceServer
	service    *extensionp1.Service
	control    *extensionLifecycle
	generation string
}

// Новая управляющая оболочка всегда начинает с disabled, включая повторное
// создание после аварии. Установку должен явно включить действующий владелец.
func (s *Store) NewExtensionP1ManagedService(ctx context.Context, host extensions.Runtime, bindings map[string]ExtensionP1MutationBinding) (*ExtensionP1ManagedService, error) {
	if !s.extensionLifecycleOwned.CompareAndSwap(false, true) {
		return nil, mutationError(codes.FailedPrecondition, "lifecycle_already_owned")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		s.extensionLifecycleOwned.Store(false)
		return nil, err
	}
	control := &extensionLifecycle{store: s, installations: map[string]*extensionLifecycleInstallation{}, instances: map[string]string{}, channels: map[string]string{}}
	for instance, b := range bindings {
		item := control.installations[b.Installation]
		if item == nil {
			coordinator, _ := lifecycle.New("disabled", 1)
			item = &extensionLifecycleInstallation{coordinator: coordinator, gate: make(chan struct{}, 1)}
			control.installations[b.Installation] = item
		}
		item.channels = append(item.channels, b.Channel)
		control.instances[instance] = b.Installation
		control.channels[instance] = b.Channel
	}
	for _, item := range control.installations {
		sort.Strings(item.channels)
	}
	svc, err := s.newExtensionP1MutationService(ctx, host, bindings, control)
	if err != nil {
		s.extensionLifecycleOwned.Store(false)
		return nil, err
	}
	return &ExtensionP1ManagedService{service: svc, control: control, generation: hex.EncodeToString(nonce[:])}, nil
}

func (s *ExtensionP1ManagedService) Generation() string { return s.generation }

func (i *extensionLifecycleInstallation) lock(ctx context.Context) (func(), error) {
	select {
	case i.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-i.gate
			return nil, err
		}
		return func() { <-i.gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *extensionLifecycle) commitGuard(ctx context.Context, installation string) (func(), error) {
	if c == nil {
		return func() {}, nil
	}
	i := c.installations[installation]
	if i == nil {
		return nil, denied()
	}
	release, err := i.lock(ctx)
	if err != nil {
		return nil, err
	}
	if i.failed.Load() {
		release()
		return nil, mutationError(codes.Unavailable, "lifecycle_failed")
	}
	// Контекст принадлежит допущенному вызову; disable/quarantine отменяют его
	// под тем же gate. Gate удерживается до завершения SQL COMMIT/ROLLBACK.
	return release, nil
}

// Change повторно аутентифицирует token; actor, роль и свидетельства клиент
// не передаёт. Метод предназначен для доверенного административного адаптера.
func (s *ExtensionP1ManagedService) Change(ctx context.Context, token, generation, installation, action string, revision int64) (lifecycle.Decision, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ctx, err := authn.AuthorizeCall(metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)), s.control.store, "/space.p1.lifecycle/Change")
	if err != nil {
		return lifecycle.Decision{}, err
	}
	if generation != s.generation {
		return lifecycle.Decision{}, mutationError(codes.FailedPrecondition, "lifecycle_generation")
	}
	i := s.control.installations[installation]
	if i == nil {
		return lifecycle.Decision{}, denied()
	}
	if action != "enable" && action != "disable" && action != "remove" {
		return lifecycle.Decision{}, mutationError(codes.FailedPrecondition, "lifecycle_action_not_connected")
	}
	release, err := i.lock(ctx)
	if err != nil {
		return lifecycle.Decision{}, mutationDatabaseError(err)
	}
	defer release()
	if i.failed.Load() {
		return lifecycle.Decision{}, mutationError(codes.Unavailable, "lifecycle_failed")
	}
	return s.control.change(ctx, i, installation, action, revision)
}

func lifecycleObservation(action, actor string, revision int64) lifecycle.Observation {
	// Только уже закреплённый пакет P1: новые digest, grants и миграции не принимаются.
	return lifecycle.Observation{Version: lifecycle.Version, Action: action, Actor: actor, Authorized: true, ExpectedRevision: revision, PackageVerified: true, Compatible: true, GrantsApproved: true}
}

func (c *extensionLifecycle) change(ctx context.Context, i *extensionLifecycleInstallation, installation, action string, revision int64) (lifecycle.Decision, error) {
	tx, err := c.store.pool.Begin(ctx)
	if err != nil {
		return lifecycle.Decision{}, mutationDatabaseError(err)
	}
	defer tx.Rollback(ctx)
	principal, err := authorizeLifecycleTx(ctx, tx, i.channels)
	if err != nil {
		return lifecycle.Decision{}, mutationDatabaseError(err)
	}
	o := lifecycleObservation(action, "owner", revision)
	d := i.coordinator.Preview(o)
	if d.Code != "OK" {
		return d, lifecycleDecisionError(d)
	}
	if err = audit(ctx, tx, principal, "extension.p1.lifecycle/"+installation+"/"+action, d.Revision); err != nil {
		return lifecycle.Decision{}, mutationDatabaseError(err)
	}
	if err = currentExtensionSession(ctx, tx); err != nil {
		return lifecycle.Decision{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		i.failed.Store(true)
		i.coordinator.Apply(lifecycleObservation("quarantine", "system", revision))
		return lifecycle.Decision{}, mutationDatabaseError(err)
	}
	// После успешного COMMIT переход обязателен, даже если клиент уже отключился.
	d = i.coordinator.Apply(o)
	if d.Code != "OK" {
		i.failed.Store(true)
		return d, mutationError(codes.Internal, "lifecycle_invariant")
	}
	return d, nil
}

func authorizeLifecycleTx(ctx context.Context, tx pgx.Tx, channels []string) (string, error) {
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='1s'; SET LOCAL statement_timeout='1500ms'`); err != nil {
		return "", err
	}
	if err := lockExtensionAuthority(ctx, tx); err != nil {
		return "", err
	}
	principal, err := managementRole(ctx, tx, true)
	if err != nil {
		return "", err
	}
	return principal, checkLifecycleChannels(ctx, tx, channels)
}

func checkLifecycleChannels(ctx context.Context, tx pgx.Tx, channels []string) error {
	for _, channel := range channels {
		if _, err := channelAccess(ctx, tx, channel, "manage", true); err != nil {
			return err
		}
	}
	return nil
}

func lifecycleDecisionError(d lifecycle.Decision) error {
	switch d.Code {
	case "Aborted":
		return mutationError(codes.Aborted, "lifecycle_revision")
	case "InvalidArgument":
		return mutationError(codes.InvalidArgument, "lifecycle_request")
	case "PermissionDenied":
		return denied()
	default:
		return mutationError(codes.FailedPrecondition, "lifecycle_state")
	}
}

// SystemTransition доступен только держателю Go-объекта в доверенном ядре;
// административное действие клиента не может выбрать роль system.
func (s *ExtensionP1ManagedService) SystemTransition(ctx context.Context, installation, action string, revision int64) (lifecycle.Decision, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if action != "quarantine" && action != "finish_disable" {
		return lifecycle.Decision{}, mutationError(codes.FailedPrecondition, "lifecycle_action_not_connected")
	}
	i := s.control.installations[installation]
	if i == nil {
		return lifecycle.Decision{}, denied()
	}
	release, err := i.lock(ctx)
	if err != nil {
		return lifecycle.Decision{}, mutationDatabaseError(err)
	}
	defer release()
	if i.failed.Load() {
		return lifecycle.Decision{}, mutationError(codes.Unavailable, "lifecycle_failed")
	}
	d := i.coordinator.Apply(lifecycleObservation(action, "system", revision))
	if d.Code != "OK" {
		return d, lifecycleDecisionError(d)
	}
	return d, nil
}
