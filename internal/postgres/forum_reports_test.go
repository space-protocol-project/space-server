package postgres

import (
	"connectrpc.com/connect"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestForumReportsConnectAndReadOnlyParticipant(t *testing.T) {
	f := newChannelFixture(t)
	section := forumSection(t, f, "reports_connect")
	topic := createTopic(t, pb.NewForumServiceClient(f.conn), f.owner, section.Id, "connect-report")
	member, _ := f.enroll(t, true)
	f.acl(t, section, roleRule("member", permissions(true, true, false, false)))
	server := httptest.NewServer(transport.WithConnect(http.NotFoundHandler(), f.conn, f.store))
	defer server.Close()
	c := rpc.NewForumServiceClient(server.Client(), server.URL)
	md, _ := metadata.FromOutgoingContext(member)
	report := connect.NewRequest(&pb.ReportForumContentRequest{ChannelId: section.Id, ThreadId: topic.Id, Reason: "Проверить тему"})
	report.Header().Set("Authorization", md.Get("authorization")[0])
	if _, err := c.ReportForumContent(f.ctx, report); err != nil {
		t.Fatal(err)
	}
	ownerMD, _ := metadata.FromOutgoingContext(f.owner)
	list := connect.NewRequest(&pb.ListForumReportsRequest{ChannelId: section.Id})
	list.Header().Set("Authorization", ownerMD.Get("authorization")[0])
	page, err := c.ListForumReports(f.ctx, list)
	if err != nil || len(page.Msg.Reports) != 1 || page.Msg.Reports[0].ThreadTitle != topic.Title {
		t.Fatal(page, err)
	}
	decision := connect.NewRequest(&pb.ResolveForumReportRequest{ChannelId: section.Id, ReportId: page.Msg.Reports[0].Id, Decision: "dismiss", Reason: "Нарушений нет"})
	decision.Header().Set("Authorization", ownerMD.Get("authorization")[0])
	if _, err = c.ResolveForumReport(f.ctx, decision); err != nil {
		t.Fatal(err)
	}
	f.acl(t, section, roleRule("member", permissions(false, false, false, false)))
	if _, err = c.ReportForumContent(f.ctx, report); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatal(err)
	}
}

func TestForumReportsLifecyclePermissionsAndAssets(t *testing.T) {
	f := newChannelFixture(t)
	c := pb.NewForumServiceClient(f.conn)
	section := forumSection(t, f, "reports")
	other := forumSection(t, f, "other_reports")
	member, _ := f.enroll(t, true)
	root, key, _ := ed25519.GenerateKey(rand.Reader)
	limited, id := forumDevice(t, f, root, key, false)
	moderator, _ := forumDevice(t, f, root, key, true)
	f.acl(t, section, roleRule("member", permissions(true, true, true, false)), principalRule(id, permissions(true, true, true, true)))
	asset := forumFile(t, f, member, section.Id, "report-attachment")
	made, err := c.CreateForumThread(member, &pb.CreateForumThreadRequest{ChannelId: section.Id, Title: "Тема", Text: "Текст", IdempotencyKey: "report-topic", AttachmentIds: []string{asset.Id}})
	if err != nil {
		t.Fatal(err)
	}
	topic := made.Thread
	reply, err := c.ReplyForumThread(member, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "Ответ", IdempotencyKey: "report-reply"})
	if err != nil {
		t.Fatal(err)
	}
	report := &pb.ReportForumContentRequest{ChannelId: section.Id, ThreadId: topic.Id, Reason: "Нарушение правил"}
	for i := 0; i < 2; i++ {
		if _, err = c.ReportForumContent(member, report); err != nil {
			t.Fatal(err)
		}
	}
	report.ReplyId = reply.Reply.Id
	if _, err = c.ReportForumContent(member, report); err != nil {
		t.Fatal(err)
	}
	list := &pb.ListForumReportsRequest{ChannelId: section.Id, Limit: 1}
	if _, err = c.ListForumReports(member, list); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	if _, err = c.ListForumReports(limited, list); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	first, err := c.ListForumReports(moderator, list)
	if err != nil || len(first.Reports) != 1 || first.NextId == 0 {
		t.Fatal(first, err)
	}
	list.AfterId = first.NextId
	second, err := c.ListForumReports(moderator, list)
	if err != nil || len(second.Reports) != 1 || second.NextId != 0 {
		t.Fatal(second, err)
	}
	if first.Reports[0].ReplyId != "" || second.Reports[0].ReplyId != reply.Reply.Id {
		t.Fatal(first, second)
	}
	decision := &pb.ResolveForumReportRequest{ChannelId: section.Id, ReportId: first.Reports[0].Id, Decision: "hide", Reason: "Подтверждено", ExpectedRevision: reply.Thread.Revision}
	if _, err = c.ResolveForumReport(limited, decision); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	decision.ChannelId = other.Id
	if _, err = c.ResolveForumReport(f.owner, decision); status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
	decision.ChannelId = section.Id
	decision.ExpectedRevision--
	if _, err = c.ResolveForumReport(moderator, decision); status.Code(err) != codes.Aborted {
		t.Fatal(err)
	}
	decision.ExpectedRevision++
	result, err := c.ResolveForumReport(moderator, decision)
	if err != nil || result.Report.Decision != "hide" || result.Report.ModeratorId != id || result.Report.ResolvedAtUnix == 0 {
		t.Fatal(result, err)
	}
	if _, err = c.ResolveForumReport(moderator, decision); status.Code(err) != codes.Aborted {
		t.Fatal(err)
	}
	visible, err := c.GetForumThread(member, &pb.GetForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id})
	if err != nil || !visible.Thread.Hidden || visible.Thread.Text != "" || len(visible.Thread.Attachments) != 0 || visible.Replies[0].Text != "" {
		t.Fatal(visible, err)
	}
	files := pb.NewAssetServiceClient(f.conn)
	_, err = files.DownloadChunk(member, &pb.DownloadChunkRequest{AssetId: asset.Id})
	if status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
	if _, err = c.ReportForumContent(member, report); status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
	decision.ReportId = second.Reports[0].Id
	decision.Decision = "dismiss"
	decision.Reason = "Достаточно скрытия темы"
	if _, err = c.ResolveForumReport(moderator, decision); err != nil {
		t.Fatal(err)
	}
	resolved, err := c.ListForumReports(moderator, &pb.ListForumReportsRequest{ChannelId: section.Id, Resolved: true})
	if err != nil || len(resolved.Reports) != 2 {
		t.Fatal(resolved, err)
	}
	log, err := c.ListForumModeration(moderator, &pb.ListForumModerationRequest{ChannelId: section.Id, ThreadId: topic.Id})
	if err != nil || len(log.Events) != 3 || log.Events[1].ReportId != first.Reports[0].Id || log.Events[2].Action != "report_dismiss" {
		t.Fatal(log, err)
	}
	// Отзыв ACL немедленно закрывает и очередь, и решения.
	f.acl(t, section, roleRule("member", permissions(true, true, true, false)))
	if _, err = c.ListForumReports(moderator, list); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	if _, err = c.ResolveForumReport(moderator, decision); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
}

func TestForumReportsAtomicDecisionAndRace(t *testing.T) {
	f := newChannelFixture(t)
	c := pb.NewForumServiceClient(f.conn)
	section := forumSection(t, f, "report_atomic")
	topic := createTopic(t, c, f.owner, section.Id, "atomic")
	reply, err := c.ReplyForumThread(f.owner, &pb.ReplyForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id, Text: "Ответ", IdempotencyKey: "answer"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ReportForumContent(f.owner, &pb.ReportForumContentRequest{ChannelId: section.Id, ThreadId: topic.Id, ReplyId: reply.Reply.Id, Reason: "Проверить"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.ListForumReports(f.owner, &pb.ListForumReportsRequest{ChannelId: section.Id})
	if err != nil {
		t.Fatal(err)
	}
	req := &pb.ResolveForumReportRequest{ChannelId: section.Id, ReportId: page.Reports[0].Id, Decision: "hide", Reason: "Причина", ExpectedRevision: reply.Reply.Revision}
	_, err = f.store.pool.Exec(f.ctx, "ALTER TABLE forum_moderation ADD CONSTRAINT reject_report_decision CHECK(action <> 'report_hide')")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ResolveForumReport(f.owner, req); err == nil {
		t.Fatal("ожидался откат")
	}
	current, err := c.GetForumThread(f.owner, &pb.GetForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id})
	if err != nil || current.Replies[0].Hidden || current.Thread.Revision != reply.Thread.Revision {
		t.Fatal(current, err)
	}
	page, err = c.ListForumReports(f.owner, &pb.ListForumReportsRequest{ChannelId: section.Id})
	if err != nil || len(page.Reports) != 1 {
		t.Fatal(page, err)
	}
	if _, err = f.store.pool.Exec(f.ctx, "ALTER TABLE forum_moderation DROP CONSTRAINT reject_report_decision"); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := c.ResolveForumReport(f.owner, req); results <- e }()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if status.Code(e) == codes.Aborted {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	current, err = c.GetForumThread(f.owner, &pb.GetForumThreadRequest{ChannelId: section.Id, ThreadId: topic.Id})
	if err != nil || !current.Replies[0].Hidden || current.Replies[0].Text != "" || current.Thread.Hidden {
		t.Fatal(current, err)
	}
}

func TestForumReportsValidationLimitsAndUpgrade(t *testing.T) {
	f := newChannelFixture(t)
	c := pb.NewForumServiceClient(f.conn)
	section := forumSection(t, f, "report_validation")
	topic := createTopic(t, c, f.owner, section.Id, "validation")
	req := &pb.ReportForumContentRequest{ChannelId: section.Id, ThreadId: topic.Id}
	for _, reason := range []string{"", "  ", strings.Repeat("я", 513), "bad\x00text"} {
		req.Reason = reason
		if _, err := c.ReportForumContent(f.owner, req); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	req.Reason = "Причина"
	req.ReplyId = "missing"
	if _, err := c.ReportForumContent(f.owner, req); status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
	req.ReplyId = ""
	for i := 0; i < 30; i++ {
		made := createTopic(t, c, f.owner, section.Id, "limit-"+strings.Repeat("x", i))
		req.ThreadId = made.Id
		if _, err := c.ReportForumContent(f.owner, req); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.ReportForumContent(f.owner, req); err != nil {
		t.Fatal("повтор должен быть безопасен", err)
	}
	req.ThreadId = topic.Id
	if _, err := c.ReportForumContent(f.owner, req); status.Code(err) != codes.ResourceExhausted {
		t.Fatal(err)
	}
	if _, err := f.store.pool.Exec(f.ctx, "UPDATE channels SET archived=true WHERE id=$1", section.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReportForumContent(f.owner, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	// Проверяем обновление существующих данных схемы 29.
	if _, err := f.store.pool.Exec(f.ctx, "DROP TABLE forum_reports; ALTER TABLE forum_moderation DROP COLUMN report_id; DELETE FROM schema_migrations WHERE version=30"); err != nil {
		t.Fatal(err)
	}
	upgraded, _, err := Open(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var title string
	if err = upgraded.pool.QueryRow(f.ctx, "SELECT title FROM forum_threads WHERE id=$1", topic.Id).Scan(&title); err != nil || title != topic.Title {
		t.Fatal(title, err)
	}
}
