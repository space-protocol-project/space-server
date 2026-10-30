package postgres

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
)

func extensionAccessChange(t *testing.T, f *channelFixture, c *pb.Channel, kind string) extensionAuthorityFixture {
	t.Helper()
	member, id := f.enroll(t, true)
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	a := extensionAuthorityFixture{member: member}
	switch kind {
	case "acl":
		a.lock = "FROM channels WHERE id"
		a.revoke = func() error {
			_, err := f.channels.UpdateChannelAccess(f.owner, &pb.UpdateChannelAccessRequest{ChannelId: c.Id, ExpectedRevision: c.Revision, Rules: []*pb.ChannelAccessRule{roleRule("member", permissions(true, true, false, false))}})
			return err
		}
	case "membership":
		var revision int64
		if err := f.store.pool.QueryRow(f.ctx, "SELECT revision FROM memberships WHERE principal_id=$1", id).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		a.lock = "FROM memberships WHERE principal_id=$1 FOR UPDATE"
		a.revoke = func() error {
			_, err := f.admin.UpdateMember(f.owner, &pb.UpdateMemberRequest{PrincipalId: id, Role: "member", Blocked: true, ExpectedRevision: revision})
			return err
		}
	case "owner":
		f.acl(t, c, roleRule("member", permissions(true, true, false, false)))
		a.member = f.owner
		a.lock = "UPDATE space_settings SET owner_id"
		// Публичного RPC передачи владельца нет: проверяется граница записи БД.
		a.revoke = func() error {
			_, err := f.store.pool.Exec(f.ctx, "UPDATE space_settings SET owner_id=$1,revision=revision+1 WHERE singleton=true", id)
			return err
		}
	default:
		t.Fatal("Неизвестный сценарий", kind)
	}
	return a
}

func TestExtensionP1MutationAccessBeforeCommit(t *testing.T) {
	for _, kind := range []string{"acl", "membership", "owner"} {
		t.Run(kind, func(t *testing.T) {
			f := newChannelFixture(t)
			c := f.create(t, "access_before")
			a := extensionAccessChange(t, f, c, kind)
			var changed atomic.Bool
			h := &mutationProbe{run: func(context.Context) ([]byte, error) {
				if err := a.revoke(); err != nil {
					return nil, err
				}
				changed.Store(true)
				return mutationOutput(0), nil
			}}
			api := mutationClient(t, f, h, []byte{1}, c.Id)
			_, err := api.ExecuteExtensionCommand(a.member, mutationRequest("access-before-001", 0))
			requireCode(t, err, codes.PermissionDenied)
			if !changed.Load() {
				t.Fatal("Права не были изменены")
			}
			mutationCounts(t, f, 0, 0, 0, 0)
		})
	}
}

func TestExtensionP1MutationAccessAfterCommit(t *testing.T) {
	for _, kind := range []string{"acl", "membership", "owner"} {
		t.Run(kind, func(t *testing.T) {
			f := newChannelFixture(t)
			c := f.create(t, "access_after")
			a := extensionAccessChange(t, f, c, kind)
			h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
			api := mutationClient(t, f, h, []byte{1}, c.Id)
			release := extensionCommitBarrier(t, f)
			done, changed := make(chan error, 1), make(chan error, 1)
			request := mutationRequest("access-after-001", 0)
			go func() { _, err := api.ExecuteExtensionCommand(a.member, request); done <- err }()
			waitExtensionSQLLock(t, f, "INSERT INTO extension_p1_outbox")
			go func() { changed <- a.revoke() }()
			waitExtensionSQLLock(t, f, a.lock)
			release()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if err := <-changed; err != nil {
				t.Fatal(err)
			}
			mutationCounts(t, f, 1, 1, 1, 1)
			_, err := api.ExecuteExtensionCommand(a.member, request)
			requireCode(t, err, codes.PermissionDenied)
			if h.calls.Load() != 1 {
				t.Fatal("Отозванный повтор вызвал гостя")
			}
		})
	}
}

func extensionExpirySQL(a extensionAuthorityFixture, kind string) (string, string) {
	switch kind {
	case "session":
		return "UPDATE auth_sessions SET expires_at=clock_timestamp()+interval '500 milliseconds' WHERE grant_id=$1 RETURNING expires_at", a.grant
	case "grant":
		return "UPDATE device_grants SET expires_at=clock_timestamp()+interval '500 milliseconds' WHERE id=$1 RETURNING expires_at", a.grant
	case "parent":
		return "UPDATE device_grants SET expires_at=clock_timestamp()+interval '500 milliseconds' WHERE id=$1 RETURNING expires_at", a.parent
	default:
		return "UPDATE device_pairings SET expires_at=clock_timestamp()+interval '500 milliseconds' WHERE id=$1 RETURNING expires_at", a.pair
	}
}

func TestExtensionP1MutationExpiryAtCommit(t *testing.T) {
	for _, kind := range []string{"session", "grant", "parent", "pairing"} {
		t.Run(kind, func(t *testing.T) {
			f := newChannelFixture(t)
			c := f.create(t, "expiry_commit")
			f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
			a := extensionAuthority(t, f, "parent")
			deadline := make(chan time.Time, 1)
			h := &mutationProbe{run: func(ctx context.Context) ([]byte, error) {
				query, id := extensionExpirySQL(a, kind)
				var expiry time.Time
				if err := f.store.pool.QueryRow(ctx, query, id).Scan(&expiry); err != nil {
					return nil, err
				}
				deadline <- expiry
				return mutationOutput(0), nil
			}}
			api := mutationClient(t, f, h, []byte{1}, c.Id)
			release := extensionCommitBarrier(t, f)
			done := make(chan error, 1)
			go func() {
				_, err := api.ExecuteExtensionCommand(a.member, mutationRequest("expiry-commit-001", 0))
				done <- err
			}()
			waitExtensionSQLLock(t, f, "INSERT INTO extension_p1_outbox")
			waitExtensionExpiry(t, f, <-deadline)
			release()
			requireCode(t, <-done, codes.PermissionDenied)
			mutationCounts(t, f, 0, 0, 0, 0)
		})
	}
}

func waitExtensionExpiry(t *testing.T, f *channelFixture, expiry time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 1200*time.Millisecond)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var expired bool
		if err := f.store.pool.QueryRow(ctx, "SELECT clock_timestamp()>$1", expiry).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
