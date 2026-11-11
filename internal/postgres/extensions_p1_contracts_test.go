package postgres

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"testing"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/demo"
	"github.com/space-protocol-project/space-server/experiments/extensions/packagep1"
	"github.com/space-protocol-project/space-server/experiments/extensions/sdk"
	"github.com/space-protocol-project/space-server/internal/extensionp1"
	"google.golang.org/grpc/codes"
)

func contractBinding(t *testing.T, channel string, output []byte) ExtensionP1MutationBinding {
	t.Helper()
	files, err := sdk.ContractStarter(output)
	if err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := packagep1.Pack(files, key)
	if err != nil {
		t.Fatal(err)
	}
	var identity struct {
		Plugin string `json:"plugin_id"`
	}
	if json.Unmarshal(files["manifest.json"], &identity) != nil {
		t.Fatal("fixture")
	}
	verified, err := packagep1.Verify(archive, packagep1.Trust{Plugin: identity.Plugin, Keys: map[string]ed25519.PublicKey{packagep1.Fingerprint(pub): pub}})
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string][]byte{}
	for _, path := range verified.Manifest.SchemaPaths() {
		schemas[path] = verified.Files[path]
	}
	r, err := demo.FromManifest(verified.Files["manifest.json"], verified.Files["server.wasm"], schemas)
	if err != nil {
		t.Fatal(err)
	}
	return ExtensionP1MutationBinding{Installation: "install", Channel: channel, Registration: r, Capabilities: []string{"storage.own", "events.own"}}
}

func contractService(t *testing.T, f *channelFixture, h extensions.Runtime, b ExtensionP1MutationBinding) *ExtensionP1ManagedService {
	t.Helper()
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, h, map[string]ExtensionP1MutationBinding{"demo": b})
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	return s
}

func TestExtensionP1ContractsReplay(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "contracts_replay")
	b := contractBinding(t, c.Id, mutationOutput(0))
	revision := uint64(0)
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { raw := mutationOutput(revision); revision++; return raw, nil }}
	s := contractService(t, f, h, b)
	api := extensionP1RPCClient(t, f, s)
	first := mutationRequest("contracts-first-0001", 0)
	if _, err := api.ExecuteExtensionCommand(f.owner, first); err != nil {
		t.Fatal(err)
	}
	again, err := api.ExecuteExtensionCommand(f.owner, first)
	if err != nil || !again.Replayed || h.calls.Load() != 1 {
		t.Fatal(again, err)
	}
	second := mutationRequest("contracts-second-0001", 1)
	second.Command = "save"
	if _, err = api.ExecuteExtensionCommand(f.owner, second); err != nil {
		t.Fatal(err)
	}
	mutationCounts(t, f, 2, 1, 2, 2)
	reopened, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.NewExtensionP1ManagedService(f.ctx, h, map[string]ExtensionP1MutationBinding{"demo": b})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := extensionP1RPCClient(t, f, restored).ExecuteExtensionCommand(f.owner, second)
	if err != nil || !replay.Replayed || h.calls.Load() != 2 {
		t.Fatal(replay, err)
	}
	mutationCounts(t, f, 2, 1, 2, 2)
}

func TestExtensionP1ContractsAccess(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "contracts_access")
	f.acl(t, c, roleRule("member", permissions(true, true, false, false)))
	member, _ := f.enroll(t, true)
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	b := contractBinding(t, c.Id, mutationOutput(0))
	missing := b
	missing.Capabilities = []string{"storage.own"}
	if _, err := f.store.NewExtensionP1ManagedService(f.ctx, h, map[string]ExtensionP1MutationBinding{"demo": missing}); err == nil {
		t.Fatal("Неодобренные capabilities приняты")
	}
	extra := b
	extra.Events = []string{"other"}
	if _, err := f.store.NewExtensionP1ManagedService(f.ctx, h, map[string]ExtensionP1MutationBinding{"demo": extra}); err == nil {
		t.Fatal("Внешний список событий обошёл manifest")
	}
	s := contractService(t, f, h, b)
	api := extensionP1RPCClient(t, f, s)
	_, err := api.ExecuteExtensionCommand(member, mutationRequest("contracts-denied-0001", 0))
	requireCode(t, err, codes.PermissionDenied)
	_, err = api.ExecuteExtensionCommand(f.owner, extensionP1Command())
	requireCode(t, err, codes.InvalidArgument)
	plain, err := extensionp1.New(h, f.store.ExtensionP1Authorizer(map[string]string{"demo": c.Id}), map[string]demo.Registration{"demo": b.Registration})
	if err != nil {
		t.Fatal(err)
	}
	_, err = extensionP1RPCClient(t, f, plain).ExecuteExtensionCommand(f.owner, extensionP1Command())
	requireCode(t, err, codes.FailedPrecondition)
	if h.calls.Load() != 0 {
		t.Fatal("Запрещённый вызов достиг WASM")
	}
	mutationCounts(t, f, 0, 0, 0, 0)
}

func TestExtensionP1ContractsRejectOutput(t *testing.T) {
	for _, mode := range []string{"result", "event", "undeclared_event", "extra_envelope"} {
		t.Run(mode, func(t *testing.T) {
			f := newChannelFixture(t)
			c := f.create(t, "contracts_output")
			raw := mutationOutput(0)
			var output map[string]json.RawMessage
			if json.Unmarshal(raw, &output) != nil {
				t.Fatal("fixture")
			}
			switch mode {
			case "result":
				output["document"] = json.RawMessage(`{"ui_api":"space.ui.p1","extra":true}`)
			case "extra_envelope":
				output["extra"] = json.RawMessage(`true`)
			default:
				var plan map[string]any
				if json.Unmarshal(output["plan"], &plan) != nil {
					t.Fatal("fixture")
				}
				event := plan["events"].([]any)[0].(map[string]any)
				if mode == "event" {
					event["payloadJson"] = "eyJleHRyYSI6dHJ1ZX0="
				} else {
					event["type"] = "undeclared"
				}
				output["plan"], _ = json.Marshal(plan)
			}
			raw, _ = json.Marshal(output)
			h := &mutationProbe{run: func(context.Context) ([]byte, error) { return raw, nil }}
			s := contractService(t, f, h, contractBinding(t, c.Id, mutationOutput(0)))
			result, err := extensionP1RPCClient(t, f, s).ExecuteExtensionCommand(f.owner, mutationRequest("contracts-output-0001", 0))
			if err == nil || result != nil {
				t.Fatal("Некорректный ответ принят", result, err)
			}
			mutationCounts(t, f, 0, 0, 0, 0)
		})
	}
}

func TestExtensionP1ContractsRevocation(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "contracts_revoke")
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	member, _ := f.enroll(t, true)
	h := &mutationProbe{run: func(context.Context) ([]byte, error) {
		f.acl(t, c, roleRule("member", permissions(true, true, false, false)))
		return mutationOutput(0), nil
	}}
	s := contractService(t, f, h, contractBinding(t, c.Id, mutationOutput(0)))
	result, err := extensionP1RPCClient(t, f, s).ExecuteExtensionCommand(member, mutationRequest("contracts-revoke-0001", 0))
	if err == nil || result != nil {
		t.Fatal("Отзыв не остановил COMMIT")
	}
	mutationCounts(t, f, 0, 0, 0, 0)
}

func TestExtensionP1ContractsWASM(t *testing.T) {
	if os.Getenv("SPACE_P1_WORKER") == "" {
		t.Skip("Требуется изолированный worker CI")
	}
	f := newChannelFixture(t)
	c := f.create(t, "contracts_wasm")
	host, err := extensions.NewProcessHost(os.Getenv("SPACE_P1_CGROUP"), os.Getenv("SPACE_P1_WORKER"))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	s := contractService(t, f, host, contractBinding(t, c.Id, mutationOutput(0)))
	result, err := extensionP1RPCClient(t, f, s).ExecuteExtensionCommand(f.owner, mutationRequest("contracts-wasm-0001", 0))
	if err != nil || result.StateRevision != 1 {
		t.Fatal(result, err)
	}
	mutationCounts(t, f, 1, 1, 1, 1)
}
