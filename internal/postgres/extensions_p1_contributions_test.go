package postgres

import (
	"bytes"
	"os"
	"testing"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/demo"
	"github.com/space-protocol-project/space-server/experiments/extensions/sdk"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
)

func contributionService(t *testing.T, f *channelFixture, host extensions.Runtime, channel string) *ExtensionP1ManagedService {
	t.Helper()
	p, err := sdk.ContributionStarter()
	if err != nil {
		t.Fatal(err)
	}
	r, err := demo.FromManifest(p["manifest.json"], p["server.wasm"], map[string][]byte{"schemas/empty.json": p["schemas/empty.json"]})
	if err != nil {
		t.Fatal(err)
	}
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, host, map[string]ExtensionP1MutationBinding{"demo": {Installation: "install", Channel: channel, Registration: r}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestExtensionP1ContributionsAccess(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "contributions")
	f.acl(t, c, roleRule("member", permissions(true, true, false, false)))
	member, _ := f.enroll(t, true)
	h := &extensionP1Probe{}
	s := contributionService(t, f, h, c.Id)
	api := extensionP1RPCClient(t, f, s)
	r := &pb.ListExtensionContributionsRequest{InstanceId: "demo"}
	_, err := api.ListExtensionContributions(f.ctx, r)
	requireCode(t, err, codes.Unauthenticated)
	_, err = api.ListExtensionContributions(f.owner, r)
	requireCode(t, err, codes.FailedPrecondition)
	managedChange(t, f, s, "enable", 1)
	list, err := api.ListExtensionContributions(member, r)
	if err != nil || len(list.Contributions) != 2 || len(list.RegistrySha256) != 32 || h.calls.Load() != 0 {
		t.Fatal(list, err, h.calls.Load())
	}
	r.Kind = pb.ExtensionContributionKind_EXTENSION_CONTRIBUTION_KIND_SECTION
	filtered, err := api.ListExtensionContributions(member, r)
	if err != nil || len(filtered.Contributions) != 1 || filtered.Contributions[0].Id != "details" || !bytes.Equal(list.RegistrySha256, filtered.RegistrySha256) {
		t.Fatal(filtered, err)
	}
	_, err = api.QueryExtension(member, &pb.QueryExtensionRequest{InstanceId: "demo", Query: "details", PayloadJson: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.ExecuteExtensionCommand(member, &pb.ExecuteExtensionCommandRequest{InstanceId: "demo", Command: "refresh", PayloadJson: []byte(`{}`)})
	requireCode(t, err, codes.PermissionDenied)
	if h.calls.Load() != 1 {
		t.Fatal("Запрещённая команда вызвала WASM")
	}
	r.Kind = pb.ExtensionContributionKind(99)
	_, err = api.ListExtensionContributions(member, r)
	requireCode(t, err, codes.InvalidArgument)
	r.Kind = pb.ExtensionContributionKind_EXTENSION_CONTRIBUTION_KIND_UNSPECIFIED
	r.InstanceId = "unknown"
	_, err = api.ListExtensionContributions(member, r)
	requireCode(t, err, codes.PermissionDenied)
	r.InstanceId = "demo"
	f.acl(t, c, roleRule("member", permissions(false, false, false, false)))
	result, err := api.ListExtensionContributions(member, r)
	requireCode(t, err, codes.NotFound)
	if result != nil {
		t.Fatal("Выданы скрытые метаданные")
	}
	f.acl(t, c, roleRule("member", permissions(true, true, false, false)))
	_, err = f.auth.Logout(member, &pb.LogoutRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.ListExtensionContributions(member, r)
	requireCode(t, err, codes.Unauthenticated)
	managedChange(t, f, s, "disable", 2)
	_, err = api.ListExtensionContributions(f.owner, r)
	requireCode(t, err, codes.FailedPrecondition)
}

func TestExtensionP1ContributionsWASM(t *testing.T) {
	if os.Getenv("SPACE_P1_WORKER") == "" {
		t.Skip("Требуется изолированный worker CI")
	}
	f := newChannelFixture(t)
	c := f.create(t, "contributions_wasm")
	host, err := extensions.NewProcessHost(os.Getenv("SPACE_P1_CGROUP"), os.Getenv("SPACE_P1_WORKER"))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	s := contributionService(t, f, host, c.Id)
	managedChange(t, f, s, "enable", 1)
	api := extensionP1RPCClient(t, f, s)
	list, err := api.ListExtensionContributions(f.owner, &pb.ListExtensionContributionsRequest{InstanceId: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Contributions {
		response, err := api.QueryExtension(f.owner, &pb.QueryExtensionRequest{InstanceId: "demo", Query: item.Query, PayloadJson: []byte(`{}`)})
		if err != nil || !bytes.Contains(response.DocumentJson, []byte(`"command":"refresh"`)) {
			t.Fatal(response, err)
		}
	}
	_, err = api.ExecuteExtensionCommand(f.owner, &pb.ExecuteExtensionCommandRequest{InstanceId: "demo", Command: "refresh", PayloadJson: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	mutationCounts(t, f, 0, 0, 0, 0)
}
