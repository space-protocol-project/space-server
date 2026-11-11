package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"regexp"
	"sort"
	"time"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/demo"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"github.com/space-protocol-project/space-server/internal/extensionp1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

//go:embed extensions_p1_schema.sql
var extensionP1Schema string

type ExtensionP1MutationBinding struct {
	Installation, Channel string
	Registration          demo.Registration
	Events                []string
}
type extensionMutationBinding struct {
	installation, channel, command string
	packageHash                    [32]byte
	events                         map[string]bool
}
type extensionMutations struct {
	store     *Store
	bindings  map[string]extensionMutationBinding
	slots     chan struct{}
	leases    extensionRetryLeases
	lifecycle *extensionLifecycle
}

// NewExtensionP1MutationService явно создаёт только экспериментальное хранилище.
// Рабочий сервер этот конструктор не вызывает. Обновление пакета здесь запрещено.
func (s *Store) NewExtensionP1MutationService(ctx context.Context, host extensions.Runtime, bindings map[string]ExtensionP1MutationBinding) (*extensionp1.Service, error) {
	return s.newExtensionP1MutationService(ctx, host, bindings, nil)
}

func (s *Store) newExtensionP1MutationService(ctx context.Context, host extensions.Runtime, bindings map[string]ExtensionP1MutationBinding, control *extensionLifecycle) (*extensionp1.Service, error) {
	backend := &extensionMutations{store: s, bindings: make(map[string]extensionMutationBinding), slots: make(chan struct{}, 2), lifecycle: control}
	registrations := make(map[string]demo.Registration)
	channels := make(map[string]string)
	for instance, binding := range bindings {
		if !channelIDPattern.MatchString(instance) || !channelIDPattern.MatchString(binding.Installation) || !channelIDPattern.MatchString(binding.Channel) {
			return nil, mutationError(codes.InvalidArgument, "invalid_binding")
		}
		binding.Registration.Module = bytes.Clone(binding.Registration.Module)
		binding.Registration.SourceManifest = bytes.Clone(binding.Registration.SourceManifest)
		binding.Registration.SourceSchemas = cloneExtensionSchemas(binding.Registration.SourceSchemas)
		item := extensionMutationBinding{binding.Installation, binding.Channel, binding.Registration.Manifest.Command, mutationPackageHash(binding), make(map[string]bool)}
		// Полный manifest пока допускает только stateless-профиль без storage.own.
		if len(binding.Registration.SourceManifest) != 0 {
			item.command = ""
		}
		for _, event := range binding.Events {
			if !channelIDPattern.MatchString(event) {
				return nil, mutationError(codes.InvalidArgument, "invalid_event")
			}
			item.events[event] = true
		}
		backend.bindings[instance] = item
		registrations[instance], channels[instance] = binding.Registration, binding.Channel
	}
	svc, err := extensionp1.NewWithMutations(host, s.ExtensionP1Authorizer(channels), registrations, backend)
	if err != nil {
		return nil, err
	}
	if err = backend.prepare(ctx); err != nil {
		return nil, err
	}
	return svc, nil
}

func cloneExtensionSchemas(source map[string][]byte) map[string][]byte {
	if source == nil {
		return nil
	}
	result := make(map[string][]byte, len(source))
	for name, raw := range source {
		result[name] = bytes.Clone(raw)
	}
	return result
}

func mutationPackageHash(binding ExtensionP1MutationBinding) [32]byte {
	events := append([]string(nil), binding.Events...)
	sort.Strings(events)
	parts := []any{binding.Registration.Module, binding.Registration.Manifest, events}
	if len(binding.Registration.SourceManifest) != 0 {
		parts = append(parts, binding.Registration.SourceManifest)
	}
	if binding.Registration.SourceSchemas != nil {
		parts = append(parts, binding.Registration.SourceSchemas)
	}
	raw, _ := json.Marshal(parts)
	return sha256.Sum256(raw)
}

func (m *extensionMutations) prepare(ctx context.Context) error {
	tx, err := m.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, extensionP1Schema); err != nil {
		return err
	}
	var version int
	if err = tx.QueryRow(ctx, "SELECT version FROM extension_p1_meta WHERE singleton=true FOR UPDATE").Scan(&version); err != nil {
		return err
	}
	if version != 3 {
		return mutationError(codes.FailedPrecondition, "storage_version")
	}
	for instance, b := range m.bindings {
		_, err = tx.Exec(ctx, `INSERT INTO extension_p1_instances(installation,instance,channel,package_hash) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, b.installation, instance, b.channel, b.packageHash[:])
		if err != nil {
			return err
		}
		var channel string
		var hash []byte
		err = tx.QueryRow(ctx, "SELECT channel,package_hash FROM extension_p1_instances WHERE installation=$1 AND instance=$2 FOR SHARE", b.installation, instance).Scan(&channel, &hash)
		if err != nil {
			return err
		}
		if channel != b.channel || !bytes.Equal(hash, b.packageHash[:]) {
			return mutationError(codes.FailedPrecondition, "binding_changed")
		}
	}
	if m.lifecycle != nil {
		if err = m.lifecycle.restore(ctx, tx, m.bindings); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (m *extensionMutations) Handles(instance, command string) bool {
	b, ok := m.bindings[instance]
	return ok && command != "" && b.command == command
}

var mutationKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func (m *extensionMutations) Execute(ctx context.Context, request *pb.ExecuteExtensionCommandRequest, invoke func(context.Context) ([]byte, error)) (*pb.ExecuteExtensionCommandResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if authn.Token(ctx) == "" {
		return nil, mutationError(codes.Unauthenticated, "session_required")
	}
	if !validMutationRequest(request) {
		return nil, mutationError(codes.InvalidArgument, "invalid_mutation_request")
	}
	if !extensionHistoryPattern.MatchString(request.HistoryId) {
		return nil, mutationError(codes.FailedPrecondition, "history_required")
	}
	select {
	case m.slots <- struct{}{}:
		defer func() { <-m.slots }()
	default:
		return nil, mutationError(codes.ResourceExhausted, "mutation_slots")
	}
	b, ok := m.bindings[request.InstanceId]
	if !ok || b.command != request.Command {
		return nil, mutationError(codes.PermissionDenied, "permission_denied")
	}
	digest := mutationDigest(b, request)
	conn, release, err := m.lockCommand(ctx, b, request)
	if err != nil {
		return nil, mutationDatabaseError(err)
	}
	defer release()
	p, prior, err := m.inspect(ctx, conn, b, request, digest)
	if err != nil {
		return nil, mutationDatabaseError(err)
	}
	if prior != nil {
		return prior, nil
	}
	raw, err := invoke(ctx)
	if err != nil {
		return nil, err
	}
	document, plan, err := extensionp1.ParseMutation(raw, request.ExpectedStateRevision, b.events)
	if err != nil {
		return nil, err
	}
	result, err := m.commit(ctx, conn, b, request, digest, p, document, plan)
	return result, mutationDatabaseError(err)
}

func validMutationRequest(r *pb.ExecuteExtensionCommandRequest) bool {
	return r != nil && r.Effect == pb.ExtensionCommandEffect_EXTENSION_COMMAND_EFFECT_MUTATION && mutationKeyPattern.MatchString(r.IdempotencyKey) && r.ExpectedStateRevision < 1<<63-1 && len(r.PayloadJson) <= extensions.MaxJSON && extensions.CheckJSON(r.PayloadJson) == nil
}
func mutationError(code codes.Code, message string) error { return status.Error(code, message) }
