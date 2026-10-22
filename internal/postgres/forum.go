package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const forumThreadColumns = "id,channel_id,author_id,author_name,title,body,created_at,sequence,revision,closed,reply_count,attachments,pinned"
const forumReplyColumns = "id,thread_id,author_id,author_name,body,created_at,sequence,attachments"

func scanForumThread(row pgx.Row) (*pb.ForumThread, error) {
	t := new(pb.ForumThread)
	var attachments []byte
	err := row.Scan(&t.Id, &t.ChannelId, &t.AuthorId, &t.AuthorName, &t.Title, &t.Text, &t.CreatedAtUnix, &t.Sequence, &t.Revision, &t.Closed, &t.ReplyCount, &attachments, &t.Pinned)
	if err == nil {
		err = json.Unmarshal(attachments, &t.Attachments)
	}
	return t, err
}
func scanForumReply(row pgx.Row) (*pb.ForumReply, error) {
	r := new(pb.ForumReply)
	var attachments []byte
	err := row.Scan(&r.Id, &r.ThreadId, &r.AuthorId, &r.AuthorName, &r.Text, &r.CreatedAtUnix, &r.Sequence, &attachments)
	if err == nil {
		err = json.Unmarshal(attachments, &r.Attachments)
	}
	return r, err
}
func forumResultError(ctx context.Context, err error) error {
	if err == pgx.ErrNoRows {
		return status.Error(codes.NotFound, "Тема не найдена")
	}
	return databaseErrorOrNil(ctx, err)
}
func forumThread(ctx context.Context, tx pgx.Tx, channel, id string) (*pb.ForumThread, error) {
	t, err := scanForumThread(tx.QueryRow(ctx, "SELECT "+forumThreadColumns+" FROM forum_threads WHERE channel_id=$1 AND id=$2", channel, id))
	return t, forumResultError(ctx, err)
}

// Блокировка канала сериализует ответы, закрытие, архивирование и смену ACL.
// Проверка сессии и актуальных ACL выполняется заново при каждом запросе.
func (s *Store) forumTransaction(ctx context.Context, channel, action string) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = s.lockSession(ctx, tx); err == nil {
		var c *pb.Channel
		c, err = channelAccess(ctx, tx, channel, action, true)
		if err == nil {
			err = validateForumChannel(c, action)
		}
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
func validateForumChannel(c *pb.Channel, action string) error {
	if c.Views[0].Type != "forum" {
		return status.Error(codes.FailedPrecondition, "Нужен форумный раздел")
	}
	if !c.Permissions.Read {
		return forbidden()
	}
	if action != "read" && c.Archived {
		return status.Error(codes.FailedPrecondition, "Раздел архивирован")
	}
	return nil
}
func forumPage(after int64, limit int32) (int, error) {
	if after < 0 || limit < 0 || limit > 50 {
		return 0, status.Error(codes.InvalidArgument, "Некорректная страница")
	}
	if limit == 0 {
		limit = 50
	}
	return int(limit), nil
}
func validForumText(value string, max int) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) && strings.TrimSpace(value) != "" && len(value) <= max
}
func validateForumBody(text, key string) error {
	if !validForumText(text, 8192) || !validForumText(key, 128) {
		return status.Error(codes.InvalidArgument, "Нужны текст до 8192 байт и ключ до 128 байт")
	}
	return nil
}
func forumAuthorName(ctx context.Context, tx pgx.Tx) (string, error) {
	var name string
	err := tx.QueryRow(ctx, "SELECT nickname FROM memberships WHERE principal_id=$1", authn.Actor(ctx)).Scan(&name)
	return name, databaseErrorOrNil(ctx, err)
}
func forumKeyConflict() error {
	return status.Error(codes.AlreadyExists, "Ключ уже использован для другого запроса")
}

func (s *Store) ListForumThreads(ctx context.Context, req *pb.ListForumThreadsRequest) (*pb.ListForumThreadsResponse, error) {
	limit, err := forumPage(req.AfterSequence, req.Limit)
	if err != nil {
		return nil, err
	}
	tx, err := s.forumTransaction(ctx, req.ChannelId, "read")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := forumThreadPage(ctx, tx, req, limit+1)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	out := new(pb.ListForumThreadsResponse)
	for rows.Next() {
		t, err := scanForumThread(rows)
		if err != nil {
			return nil, databaseError(ctx, err)
		}
		if len(out.Threads) == limit {
			out.NextSequence = out.Threads[limit-1].Sequence
			out.NextPinned = out.Threads[limit-1].Pinned
			break
		}
		out.Threads = append(out.Threads, t)
	}
	err = rows.Err()
	rows.Close()
	if err == nil {
		err = forumReadStates(ctx, tx, out.Threads)
	}
	return out, databaseErrorOrNil(ctx, err)
}
func (s *Store) GetForumThread(ctx context.Context, req *pb.GetForumThreadRequest) (*pb.GetForumThreadResponse, error) {
	limit, err := forumPage(req.AfterSequence, req.Limit)
	if err != nil {
		return nil, err
	}
	tx, err := s.forumTransaction(ctx, req.ChannelId, "read")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	t, err := forumThread(ctx, tx, req.ChannelId, req.ThreadId)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, "SELECT "+forumReplyColumns+" FROM forum_replies WHERE thread_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3", t.Id, req.AfterSequence, limit+1)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	out := &pb.GetForumThreadResponse{Thread: t}
	for rows.Next() {
		r, err := scanForumReply(rows)
		if err != nil {
			return nil, databaseError(ctx, err)
		}
		if len(out.Replies) == limit {
			out.NextSequence = out.Replies[limit-1].Sequence
			break
		}
		out.Replies = append(out.Replies, r)
	}
	err = rows.Err()
	rows.Close()
	if err == nil {
		err = forumReadStates(ctx, tx, []*pb.ForumThread{t})
	}
	return out, databaseErrorOrNil(ctx, err)
}
func (s *Store) CreateForumThread(ctx context.Context, req *pb.CreateForumThreadRequest) (*pb.CreateForumThreadResponse, error) {
	if err := validateForumMessage(req.Text, req.IdempotencyKey, req.AttachmentIds); err != nil {
		return nil, err
	}
	if !validForumText(req.Title, 320) {
		return nil, status.Error(codes.InvalidArgument, "Нужен заголовок до 320 байт")
	}
	tx, err := s.forumTransaction(ctx, req.ChannelId, "write")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	old, err := scanForumThread(tx.QueryRow(ctx, "SELECT "+forumThreadColumns+" FROM forum_threads WHERE channel_id=$1 AND author_id=$2 AND idempotency_key=$3", req.ChannelId, authn.Actor(ctx), req.IdempotencyKey))
	if err == nil {
		if old.Title != req.Title || old.Text != req.Text || !forumSameAssets(old.Attachments, req.AttachmentIds) {
			return nil, forumKeyConflict()
		}
		if err = forumReadStates(ctx, tx, []*pb.ForumThread{old}); err != nil {
			return nil, databaseError(ctx, err)
		}
		return &pb.CreateForumThreadResponse{Thread: old}, nil
	}
	if err != pgx.ErrNoRows {
		return nil, databaseError(ctx, err)
	}
	assets, err := contentAssets(ctx, tx, &pb.CreateContentRequest{ChannelId: req.ChannelId, AttachmentIds: req.AttachmentIds})
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(assets)
	id, err := randomString("topic_")
	if err != nil {
		return nil, status.Error(codes.Internal, "Не удалось создать идентификатор")
	}
	name, err := forumAuthorName(ctx, tx)
	if err != nil {
		return nil, err
	}
	var sequence int64
	if err = tx.QueryRow(ctx, "UPDATE channels SET next_sequence=next_sequence+1 WHERE id=$1 RETURNING next_sequence-1", req.ChannelId).Scan(&sequence); err != nil {
		return nil, databaseError(ctx, err)
	}
	t, err := scanForumThread(tx.QueryRow(ctx, "INSERT INTO forum_threads(id,channel_id,author_id,author_name,title,body,sequence,idempotency_key,attachments) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING "+forumThreadColumns, id, req.ChannelId, authn.Actor(ctx), name, req.Title, req.Text, sequence, req.IdempotencyKey, encoded))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = bindForumAssets(ctx, tx, assets, t.Id, true); err != nil {
		return nil, err
	}
	if err = forumReadStates(ctx, tx, []*pb.ForumThread{t}); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.CreateForumThreadResponse{Thread: t}, databaseErrorOrNil(ctx, tx.Commit(ctx))
}
func (s *Store) ReplyForumThread(ctx context.Context, req *pb.ReplyForumThreadRequest) (*pb.ReplyForumThreadResponse, error) {
	if err := validateForumMessage(req.Text, req.IdempotencyKey, req.AttachmentIds); err != nil {
		return nil, err
	}
	tx, err := s.forumTransaction(ctx, req.ChannelId, "write")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	t, err := forumThread(ctx, tx, req.ChannelId, req.ThreadId)
	if err != nil {
		return nil, err
	}
	old, err := scanForumReply(tx.QueryRow(ctx, "SELECT "+forumReplyColumns+" FROM forum_replies WHERE channel_id=$1 AND author_id=$2 AND idempotency_key=$3", req.ChannelId, authn.Actor(ctx), req.IdempotencyKey))
	if err == nil {
		if old.ThreadId != req.ThreadId || old.Text != req.Text || !forumSameAssets(old.Attachments, req.AttachmentIds) {
			return nil, forumKeyConflict()
		}
		if err = forumReadStates(ctx, tx, []*pb.ForumThread{t}); err != nil {
			return nil, databaseError(ctx, err)
		}
		return &pb.ReplyForumThreadResponse{Reply: old, Thread: t}, nil
	}
	if err != pgx.ErrNoRows {
		return nil, databaseError(ctx, err)
	}
	if t.Closed {
		return nil, status.Error(codes.FailedPrecondition, "Тема закрыта")
	}
	assets, err := contentAssets(ctx, tx, &pb.CreateContentRequest{ChannelId: req.ChannelId, AttachmentIds: req.AttachmentIds})
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(assets)
	id, err := randomString("reply_")
	if err != nil {
		return nil, status.Error(codes.Internal, "Не удалось создать идентификатор")
	}
	name, err := forumAuthorName(ctx, tx)
	if err != nil {
		return nil, err
	}
	r, err := scanForumReply(tx.QueryRow(ctx, "INSERT INTO forum_replies(id,channel_id,thread_id,author_id,author_name,body,sequence,idempotency_key,attachments) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING "+forumReplyColumns, id, req.ChannelId, t.Id, authn.Actor(ctx), name, req.Text, t.ReplyCount+1, req.IdempotencyKey, encoded))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	t, err = scanForumThread(tx.QueryRow(ctx, "UPDATE forum_threads SET reply_count=reply_count+1,revision=revision+1 WHERE id=$1 RETURNING "+forumThreadColumns, t.Id))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = bindForumAssets(ctx, tx, assets, r.Id, false); err != nil {
		return nil, err
	}
	if err = forumReadStates(ctx, tx, []*pb.ForumThread{t}); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.ReplyForumThreadResponse{Reply: r, Thread: t}, databaseErrorOrNil(ctx, tx.Commit(ctx))
}
func (s *Store) CloseForumThread(ctx context.Context, req *pb.CloseForumThreadRequest) (*pb.CloseForumThreadResponse, error) {
	tx, err := s.forumTransaction(ctx, req.ChannelId, "manage")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	t, err := forumThread(ctx, tx, req.ChannelId, req.ThreadId)
	if err != nil {
		return nil, err
	}
	if req.ExpectedRevision != t.Revision {
		return nil, status.Error(codes.Aborted, "Тема изменилась; обновите её перед закрытием")
	}
	if t.Closed {
		if err = forumReadStates(ctx, tx, []*pb.ForumThread{t}); err != nil {
			return nil, databaseError(ctx, err)
		}
		return &pb.CloseForumThreadResponse{Thread: t}, nil
	}
	t, err = scanForumThread(tx.QueryRow(ctx, "UPDATE forum_threads SET closed=true,revision=revision+1 WHERE id=$1 RETURNING "+forumThreadColumns, t.Id))
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = audit(ctx, tx, authn.Actor(ctx), "forum.close:"+req.ChannelId+":"+t.Id, t.Revision); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = forumReadStates(ctx, tx, []*pb.ForumThread{t}); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.CloseForumThreadResponse{Thread: t}, databaseErrorOrNil(ctx, tx.Commit(ctx))
}

func validateForumMessage(text, key string, ids []string) error {
	if text == "" && len(ids) > 0 {
		return validateForumBody("attachment", key)
	}
	return validateForumBody(text, key)
}
func forumSameAssets(assets []*pb.Asset, ids []string) bool {
	if len(assets) != len(ids) {
		return false
	}
	for i, a := range assets {
		if a.Id != ids[i] {
			return false
		}
	}
	return true
}
func bindForumAssets(ctx context.Context, tx pgx.Tx, assets []*pb.Asset, id string, thread bool) error {
	column := "forum_reply_id"
	if thread {
		column = "forum_thread_id"
	}
	for _, a := range assets {
		if _, err := tx.Exec(ctx, "UPDATE assets SET "+column+"=$2 WHERE id=$1", a.Id, id); err != nil {
			return databaseError(ctx, err)
		}
	}
	return nil
}
