package postgres

import (
	"context"
	"testing"
	"time"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

func registryAPIFixture(t *testing.T) (*channelFixture, *ExtensionP1ManagedService, pb.ExtensionP1ServiceClient) {
	t.Helper()
	f := newChannelFixture(t)
	h := &mutationProbe{run: func(context.Context) ([]byte, error) {
		t.Error("Административный RPC вызвал гостя")
		return mutationOutput(0), nil
	}}
	s, api := managedFixture(t, f, h)
	return f, s, api
}

func registryAPICalls(api pb.ExtensionP1ServiceClient, generation string) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"list": func(ctx context.Context) error {
			_, err := api.ListExtensionInstallations(ctx, &pb.ListExtensionInstallationsRequest{})
			return err
		},
		"get": func(ctx context.Context) error {
			_, err := api.GetExtensionInstallation(ctx, &pb.GetExtensionInstallationRequest{InstallationId: "install"})
			return err
		},
		"audit": func(ctx context.Context) error {
			_, err := api.ListExtensionInstallationAudit(ctx, &pb.ListExtensionInstallationAuditRequest{InstallationId: "install", Generation: generation})
			return err
		},
		"change": func(ctx context.Context) error {
			_, err := api.ChangeExtensionInstallation(ctx, &pb.ChangeExtensionInstallationRequest{InstallationId: "install", Generation: generation, ExpectedRevision: 1, Action: "enable"})
			return err
		},
	}
}

func TestExtensionP1RegistryAPIPermissions(t *testing.T) {
	for _, test := range []struct {
		name, sql string
		code      codes.Code
	}{
		{"anonymous", "", codes.Unauthenticated},
		{"invalid", "", codes.Unauthenticated},
		{"member", "", codes.PermissionDenied},
		{"admin", "UPDATE memberships SET role='admin' WHERE principal_id=$1", codes.PermissionDenied},
		{"blocked", "UPDATE memberships SET blocked=true WHERE principal_id=$1", codes.PermissionDenied},
		{"scope", "UPDATE device_grants SET scopes=ARRAY['chat.read','chat.write'] WHERE principal_id=$1", codes.PermissionDenied},
		{"expired", "UPDATE auth_sessions SET expires_at=now()-interval '1 second' WHERE grant_id IN (SELECT id FROM device_grants WHERE principal_id=$1)", codes.Unauthenticated},
		{"revoked", "UPDATE device_grants SET revoked_at=now() WHERE principal_id=$1", codes.Unauthenticated},
		{"epoch", "UPDATE principals SET auth_epoch=auth_epoch+1 WHERE id=$1", codes.Unauthenticated},
		{"push_only", "UPDATE auth_sessions SET push_only=true WHERE grant_id IN (SELECT id FROM device_grants WHERE principal_id=$1)", codes.Unauthenticated},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, s, api := registryAPIFixture(t)
			ctx, id := f.owner, f.ownerID
			switch test.name {
			case "anonymous":
				ctx = f.ctx
			case "invalid":
				ctx = metadata.AppendToOutgoingContext(f.ctx, "authorization", "Bearer invalid")
			case "member", "admin", "blocked":
				ctx, id = f.enroll(t, true)
			}
			if test.sql != "" {
				if _, err := f.store.pool.Exec(f.ctx, test.sql, id); err != nil {
					t.Fatal(err)
				}
			}
			for name, call := range registryAPICalls(api, s.Generation()) {
				t.Run(name, func(t *testing.T) { requireCode(t, call(ctx), test.code) })
			}
			lifecycleAuditCount(t, f, 0)
			lifecycleRegistryState(t, f, "disabled", 1)
		})
	}
}

func TestExtensionP1RegistryAPITransitionsAndAudit(t *testing.T) {
	f, s, api := registryAPIFixture(t)
	initial, err := api.GetExtensionInstallation(f.owner, &pb.GetExtensionInstallationRequest{InstallationId: "install"})
	if err != nil || initial.GetInstallation().GetState() != "disabled" || len(initial.GetInstallation().GetBindingSha256()) != 32 {
		t.Fatal(initial, err)
	}
	change := func(action string, revision int64, want string) {
		t.Helper()
		result, err := api.ChangeExtensionInstallation(f.owner, &pb.ChangeExtensionInstallationRequest{InstallationId: "install", Generation: s.Generation(), ExpectedRevision: revision, Action: action})
		if err != nil || result.GetState() != want || result.GetRevision() != revision+1 {
			t.Fatal(result, err)
		}
	}
	change("enable", 1, "enabled")
	first, err := api.ListExtensionInstallationAudit(f.owner, &pb.ListExtensionInstallationAuditRequest{InstallationId: "install", Generation: s.Generation(), PageSize: 1})
	if err != nil || len(first.GetEntries()) != 1 || first.GetNextAfterRevision() != 1 || first.GetThroughRevision() != 2 {
		t.Fatal(first, err)
	}
	if first.Entries[0].Actor != "checkpoint" || first.Entries[0].PrincipalId != "" {
		t.Fatal(first)
	}
	change("disable", 2, "disabling")
	next, err := api.ListExtensionInstallationAudit(f.owner, &pb.ListExtensionInstallationAuditRequest{InstallationId: "install", Generation: s.Generation(), AfterRevision: 1, ThroughRevision: first.ThroughRevision, PageSize: 1})
	if err != nil || len(next.GetEntries()) != 1 || next.GetNextAfterRevision() != 0 || next.Entries[0].Action != "enable" || next.Entries[0].PrincipalId != f.ownerID {
		t.Fatal(next, err)
	}
	if _, err = s.SystemTransition(f.ctx, "install", "finish_disable", 3); err != nil {
		t.Fatal(err)
	}
	change("remove", 4, "removed")
	all, err := api.ListExtensionInstallationAudit(f.owner, &pb.ListExtensionInstallationAuditRequest{InstallationId: "install", Generation: s.Generation()})
	if err != nil || len(all.GetEntries()) != 5 || all.GetThroughRevision() != 5 {
		t.Fatal(all, err)
	}
	if all.Entries[3].Actor != "system" || all.Entries[3].PrincipalId != "" {
		t.Fatal(all)
	}
	for _, entry := range all.Entries {
		if _, err := time.Parse(time.RFC3339Nano, entry.CreatedAt); err != nil {
			t.Fatal(err)
		}
	}
	lifecycleAuditCount(t, f, 3)
}

func TestExtensionP1RegistryAPIRejectsStaleAndInvalid(t *testing.T) {
	f, s, api := registryAPIFixture(t)
	r := &pb.ChangeExtensionInstallationRequest{InstallationId: "install", Generation: s.Generation(), ExpectedRevision: 1, Action: "enable"}
	if _, err := api.ChangeExtensionInstallation(f.owner, r); err != nil {
		t.Fatal(err)
	}
	_, err := api.ChangeExtensionInstallation(f.owner, r)
	requireCode(t, err, codes.Aborted)
	r.Generation = "00000000000000000000000000000000"
	_, err = api.ChangeExtensionInstallation(f.owner, r)
	requireCode(t, err, codes.FailedPrecondition)
	for _, action := range []string{"quarantine", "finish_disable", "begin_upgrade", "install", ""} {
		r.Action = action
		_, err = api.ChangeExtensionInstallation(f.owner, r)
		requireCode(t, err, codes.InvalidArgument)
	}
	_, err = api.GetExtensionInstallation(f.owner, &pb.GetExtensionInstallationRequest{InstallationId: "unknown"})
	requireCode(t, err, codes.PermissionDenied)
	_, err = api.ChangeExtensionInstallation(f.owner, &pb.ChangeExtensionInstallationRequest{InstallationId: "unknown", Generation: s.Generation(), ExpectedRevision: 1, Action: "enable"})
	requireCode(t, err, codes.PermissionDenied)
	_, err = api.ListExtensionInstallationAudit(f.owner, &pb.ListExtensionInstallationAuditRequest{InstallationId: "unknown", Generation: s.Generation()})
	requireCode(t, err, codes.PermissionDenied)
	_, err = api.ListExtensionInstallations(f.owner, &pb.ListExtensionInstallationsRequest{PageSize: 101})
	requireCode(t, err, codes.InvalidArgument)
	_, err = api.ListExtensionInstallations(f.owner, &pb.ListExtensionInstallationsRequest{AfterInstallationId: "install"})
	requireCode(t, err, codes.FailedPrecondition)
	_, err = api.ListExtensionInstallationAudit(f.owner, &pb.ListExtensionInstallationAuditRequest{InstallationId: "install", Generation: s.Generation(), AfterRevision: 1})
	requireCode(t, err, codes.InvalidArgument)
	_, err = api.ListExtensionInstallationAudit(f.owner, &pb.ListExtensionInstallationAuditRequest{InstallationId: "install", Generation: "old"})
	requireCode(t, err, codes.FailedPrecondition)
	lifecycleAuditCount(t, f, 1)
}
