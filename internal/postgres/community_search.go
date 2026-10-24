package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type communityCursor struct {
	Version int    `json:"v"`
	Binding string `json:"b"`
	Created int64  `json:"t"`
	Kind    int32  `json:"k"`
	ID      string `json:"i"`
}

func searchBinding(channel, query, actor string) string {
	hash := sha256.Sum256([]byte(channel + "\x00" + query + "\x00" + actor))
	return hex.EncodeToString(hash[:])
}

func parseCommunityCursor(token, binding string) (communityCursor, error) {
	if token == "" {
		return communityCursor{}, nil
	}
	invalid := status.Error(codes.InvalidArgument, "Некорректная страница поиска")
	if len(token) > 1024 {
		return communityCursor{}, invalid
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		return communityCursor{}, invalid
	}
	var cursor communityCursor
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF {
		return cursor, invalid
	}
	if cursor.Version != 1 || cursor.Binding != binding || cursor.Created < 0 || cursor.Kind < 1 || cursor.Kind > 4 || !validForumText(cursor.ID, 256) {
		return cursor, invalid
	}
	return cursor, nil
}

func communityPageToken(result *pb.CommunitySearchResult, binding string) string {
	data, _ := json.Marshal(communityCursor{1, binding, result.CreatedAtUnix, int32(result.Kind), result.Id})
	return base64.RawURLEncoding.EncodeToString(data)
}

// Используется тот же Unicode fold, что и в индексе поиска чата. Один символ
// ищется без gram-фильтра: китайский запрос не обязан содержать два символа.
const communityMatch = `strpos(search_text,space_search_fold_v1($2))>0
 AND (char_length($2)=1 OR space_search_terms_v1(search_text,2,3) @>
 space_search_terms_v1(space_search_fold_v1($2),LEAST(3,char_length($2)),3))`

const forumSearchRows = `SELECT 1 AS kind,id,id AS parent_id,title,body,author_id,author_name,created_at,sequence
 FROM forum_threads WHERE channel_id=$1 AND NOT hidden AND ` + communityMatch + `
 UNION ALL
 SELECT 2,r.id,t.id,t.title,r.body,r.author_id,r.author_name,r.created_at,r.sequence
 FROM (SELECT * FROM forum_replies WHERE NOT hidden AND ` + communityMatch + `) r
 JOIN forum_threads t ON t.id=r.thread_id WHERE t.channel_id=$1 AND NOT t.hidden`

const feedSearchRows = `SELECT 3 AS kind,id,id AS parent_id,'' AS title,body,author_id,author_name,created_at,sequence
 FROM feed_posts WHERE channel_id=$1 AND NOT hidden AND ` + communityMatch + `
 UNION ALL
 SELECT 4,r.id,p.id,left(p.body,120),r.body,r.author_id,r.author_name,r.created_at,r.sequence
 FROM (SELECT * FROM feed_comments WHERE NOT hidden AND ` + communityMatch + `) r
 JOIN feed_posts p ON p.id=r.post_id WHERE p.channel_id=$1 AND NOT p.hidden`

func communitySearchSQL(kind string) string {
	rows := forumSearchRows
	if kind == "feed" {
		rows = feedSearchRows
	}
	return `SELECT kind,id,parent_id,title,body,author_id,author_name,created_at,sequence FROM (` + rows + `) results
 WHERE (NOT $3 OR (created_at,kind,id COLLATE "C") < ($4,$5,$6 COLLATE "C"))
 ORDER BY created_at DESC,kind DESC,id COLLATE "C" DESC LIMIT $7`
}

func (s *Store) searchTransaction(ctx context.Context, channel string) (pgx.Tx, string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, "", databaseError(ctx, err)
	}
	if err = s.lockSession(ctx, tx); err == nil {
		var c *pb.Channel
		c, err = channelAccess(ctx, tx, channel, "read", true)
		if err == nil {
			kind := c.Views[0].Type
			if !c.Archived && (kind == "forum" || kind == "feed") {
				return tx, kind, nil
			}
			err = status.Error(codes.FailedPrecondition, "Поиск доступен в открытом разделе форума или ленты")
		}
	}
	tx.Rollback(ctx)
	return nil, "", err
}

func (s *Store) SearchCommunityContent(ctx context.Context, req *pb.SearchCommunityContentRequest) (*pb.SearchCommunityContentResponse, error) {
	query := strings.TrimSpace(req.Query)
	if !validForumText(query, 256) || req.Limit < 0 || req.Limit > 50 {
		return nil, status.Error(codes.InvalidArgument, "Нужны текст до 256 байт и страница до 50 результатов")
	}
	limit := int(req.Limit)
	if limit == 0 {
		limit = 20
	}
	binding := searchBinding(req.ChannelId, query, authn.Actor(ctx))
	cursor, err := parseCommunityCursor(req.PageToken, binding)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, kind, err := s.searchTransaction(ctx, req.ChannelId)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, communitySearchSQL(kind), req.ChannelId, query, req.PageToken != "", cursor.Created, cursor.Kind, cursor.ID, limit+1)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	out := new(pb.SearchCommunityContentResponse)
	for rows.Next() {
		r := new(pb.CommunitySearchResult)
		var body string
		if err = rows.Scan(&r.Kind, &r.Id, &r.ParentId, &r.Title, &body, &r.AuthorId, &r.AuthorName, &r.CreatedAtUnix, &r.Sequence); err != nil {
			return nil, databaseError(ctx, err)
		}
		if len(out.Results) == limit {
			out.NextPageToken = communityPageToken(out.Results[limit-1], binding)
			break
		}
		r.Snippet = communitySnippet(body, query)
		out.Results = append(out.Results, r)
	}
	return out, databaseErrorOrNil(ctx, rows.Err())
}

func communitySnippet(body, query string) string {
	text := []rune(body)
	start := 0
	// strings.ToLower сохраняет число Unicode code points; позиции фрагмента
	// остаются корректными для кириллицы, emoji и китайского текста.
	if index := strings.Index(strings.ToLower(body), strings.ToLower(query)); index >= 0 {
		start = len([]rune(strings.ToLower(body)[:index])) - 32
		if start < 0 {
			start = 0
		}
	}
	end := min(len(text), start+240)
	return string(text[start:end])
}
