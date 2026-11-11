package postgres

import (
	"testing"

	"google.golang.org/grpc/codes"
)

func TestBuiltinRegistryViewChange(t *testing.T) {
	f := newChannelFixture(t)
	c := feedSection(t, f, "builtin_change")
	post := feedCreate(t, f, f.owner, c.Id, "preserved")
	ctx := builtinContext(t, f, f.owner)
	oldID := "builtin:" + c.Id + ":feed"
	newID := "builtin:" + c.Id + ":chat"
	for _, commit := range []bool{false, true} {
		tx, err := f.store.pool.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(f.ctx, "UPDATE channels SET view_type='chat',mls_group_id=decode(repeat('ab',16),'hex') WHERE id=$1", c.Id); err != nil {
			tx.Rollback(f.ctx)
			t.Fatal(err)
		}
		var count int
		err = tx.QueryRow(f.ctx, "SELECT count(*) FROM builtin_extension_instances WHERE id=$1", newID).Scan(&count)
		if err != nil || count != 1 {
			tx.Rollback(f.ctx)
			t.Fatal("Нет новой привязки", count, err)
		}
		if commit {
			err = tx.Commit(f.ctx)
		} else {
			err = tx.Rollback(f.ctx)
		}
		if err != nil {
			t.Fatal(err)
		}
		available, missing := oldID, newID
		if commit {
			available, missing = newID, oldID
		}
		if _, err = f.store.BuiltinSection(ctx, available); err != nil {
			t.Fatal(err)
		}
		_, err = f.store.BuiltinSection(ctx, missing)
		requireCode(t, err, codes.NotFound)
		if err = f.store.pool.QueryRow(f.ctx, "SELECT count(*) FROM feed_posts WHERE id=$1", post.Id).Scan(&count); err != nil || count != 1 {
			t.Fatal("Изменено содержимое", count, err)
		}
	}
}
