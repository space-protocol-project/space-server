package postgres

import (
	"context"
	"testing"
	"time"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
)

// SQL-барьер внутри outbox помещает logout точно между записью данных и commit.
func TestExtensionP1MutationCommitOrdersLogout(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "mut_order")
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	member, _ := f.enroll(t, true)
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	api := mutationClient(t, f, h, []byte{1}, c.Id)
	gate, err := f.store.pool.Acquire(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Release()
	if _, err = gate.Exec(f.ctx, "SELECT pg_advisory_lock(974421)"); err != nil {
		t.Fatal(err)
	}
	defer gate.Exec(context.Background(), "SELECT pg_advisory_unlock(974421)")
	_, err = f.store.pool.Exec(f.ctx, `CREATE FUNCTION extension_p1.wait_commit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_advisory_xact_lock(974421); RETURN NEW; END$$;
 CREATE TRIGGER wait_commit BEFORE INSERT ON extension_p1.outbox FOR EACH ROW EXECUTE FUNCTION extension_p1.wait_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	request := mutationRequest("ordered-command-001", 0)
	go func() { _, err := api.ExecuteExtensionCommand(member, request); done <- err }()
	waitExtensionSQLLock(t, f, "INSERT INTO extension_p1.outbox")
	logout := make(chan error, 1)
	go func() { _, err := f.auth.Logout(member, &pb.LogoutRequest{}); logout <- err }()
	waitExtensionSQLLock(t, f, "DELETE FROM auth_sessions")
	if _, err = gate.Exec(f.ctx, "SELECT pg_advisory_unlock(974421)"); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = <-logout; err != nil {
		t.Fatal(err)
	}
	mutationCounts(t, f, 1, 1, 1, 1)
	_, err = api.ExecuteExtensionCommand(member, request)
	requireCode(t, err, codes.Unauthenticated)
}

func waitExtensionSQLLock(t *testing.T, f *channelFixture, fragment string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		err := f.store.pool.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND position($1 in query)>0)`, fragment).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Не достигнут SQL-барьер", fragment)
}
