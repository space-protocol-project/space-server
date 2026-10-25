package postgres

import (
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"testing"
)

func TestPersonalNotificationsUpgradeSkipsRemovedPrincipal(t *testing.T) {
	f := newChannelFixture(t)
	recipient, m := mentionRecipient(t, f, "Маша")
	valid := mentionedChat(t, f, m, "valid")
	former, err := f.contents.CreateContent(f.owner, &pb.CreateContentRequest{ChannelId: "general", Text: "@ушёл", IdempotencyKey: "former"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.pool.Exec(f.ctx, dropPersonalNotificationsSchema); err != nil {
		t.Fatal(err)
	}
	// В схеме 35 аннотация чата не имела FK на адресата: профиль мог быть удалён.
	if _, err = f.store.pool.Exec(f.ctx, `UPDATE contents SET mentions='[{"principal_id":"u_removed","display_name":"ушёл","start":0,"length":5}]' WHERE id=$1`, former.Content.Id); err != nil {
		t.Fatal(err)
	}
	upgraded, identity, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if identity.ServerID != f.identity.ServerID {
		t.Fatal("Изменилась идентичность")
	}
	p := notificationPage(t, f, recipient, &pb.ListPersonalNotificationsRequest{ChannelId: "general"})
	if len(p.Notifications) != 1 || p.Notifications[0].ContentId != valid.Id {
		t.Fatal(p)
	}
	var index bool
	if err = upgraded.pool.QueryRow(f.ctx, "SELECT to_regclass(current_schema()||'.personal_notifications_content') IS NOT NULL").Scan(&index); err != nil || !index {
		t.Fatal(index, err)
	}
}

func TestPersonalNotificationsEditCreatesOnlyNewRecipient(t *testing.T) {
	f := newChannelFixture(t)
	first, m1 := mentionRecipient(t, f, "Маша")
	second, m2 := mentionRecipient(t, f, "Ира")
	message := mentionedChat(t, f, m1, "edit-recipient")
	before := notificationPage(t, f, first, &pb.ListPersonalNotificationsRequest{ChannelId: "general"})
	changed, err := f.contents.UpdateContent(f.owner, &pb.UpdateContentRequest{ChannelId: "general", Id: message.Id, ExpectedRevision: 1, Text: "@Ира", Mentions: []*pb.CommunityMention{m2}})
	if err != nil {
		t.Fatal(err)
	}
	if p := notificationPage(t, f, first, &pb.ListPersonalNotificationsRequest{ChannelId: "general"}); len(p.Notifications) != 0 || p.ThroughSequence != 1 {
		t.Fatal(p)
	}
	other := notificationPage(t, f, second, &pb.ListPersonalNotificationsRequest{ChannelId: "general"})
	if len(other.Notifications) != 1 || other.Notifications[0].UnreadReasons != 1 {
		t.Fatal(other)
	}
	_, err = f.contents.UpdateContent(f.owner, &pb.UpdateContentRequest{ChannelId: "general", Id: message.Id, ExpectedRevision: changed.Content.Revision, Text: "@Маша", Mentions: []*pb.CommunityMention{m1}})
	if err != nil {
		t.Fatal(err)
	}
	restored := notificationPage(t, f, first, &pb.ListPersonalNotificationsRequest{ChannelId: "general"})
	if len(restored.Notifications) != 1 || restored.ThroughSequence != 1 || restored.Notifications[0].Id != before.Notifications[0].Id {
		t.Fatal(restored)
	}
	if _, err = f.store.pool.Exec(f.ctx, "UPDATE memberships SET nickname='Я' WHERE principal_id=$1", f.ownerID); err != nil {
		t.Fatal(err)
	}
	mentionedChat(t, f, &pb.CommunityMention{PrincipalId: f.ownerID, DisplayName: "Я", Length: 2}, "self")
	if p := notificationPage(t, f, f.owner, &pb.ListPersonalNotificationsRequest{ChannelId: "general"}); len(p.Notifications) != 0 || p.ThroughSequence != 0 {
		t.Fatal(p)
	}
}
