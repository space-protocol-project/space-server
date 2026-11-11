package postgres

import (
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
)

func TestExtensionP1ContributionsSessions(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "contribution_sessions")
	f.acl(t, c, roleRule("member", permissions(true, true, false, false)))
	h := &extensionP1Probe{}
	s := contributionService(t, f, h, c.Id)
	managedChange(t, f, s, "enable", 1)
	api := extensionP1RPCClient(t, f, s)
	for _, test := range []struct {
		name, sql string
		code      codes.Code
	}{
		{"expired", "UPDATE auth_sessions SET expires_at=now()-interval '1 second' WHERE grant_id IN (SELECT id FROM device_grants WHERE principal_id=$1)", codes.Unauthenticated},
		{"revoked", "UPDATE device_grants SET revoked_at=now() WHERE principal_id=$1", codes.Unauthenticated},
		{"epoch", "UPDATE principals SET auth_epoch=auth_epoch+1 WHERE id=$1", codes.Unauthenticated},
		{"push_only", "UPDATE auth_sessions SET push_only=true WHERE grant_id IN (SELECT id FROM device_grants WHERE principal_id=$1)", codes.Unauthenticated},
		{"scopes", "UPDATE device_grants SET scopes=ARRAY['identity.recover'] WHERE principal_id=$1", codes.PermissionDenied},
		{"blocked", "UPDATE memberships SET blocked=true,revision=revision+1 WHERE principal_id=$1", codes.PermissionDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			member, id := f.enroll(t, true)
			r := &pb.ListExtensionContributionsRequest{InstanceId: "demo"}
			if _, err := api.ListExtensionContributions(member, r); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.pool.Exec(f.ctx, test.sql, id); err != nil {
				t.Fatal(err)
			}
			result, err := api.ListExtensionContributions(member, r)
			requireCode(t, err, test.code)
			if result != nil || h.calls.Load() != 0 {
				t.Fatal("Выданы метаданные без допуска", result, h.calls.Load())
			}
		})
	}
}
