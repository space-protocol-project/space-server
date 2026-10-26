package postgres

import (
    "context"
    "strings"
    "time"
    "github.com/jackc/pgx/v5"
    pb "github.com/space-protocol-project/space-server/gen/space/v1"
    "github.com/space-protocol-project/space-server/internal/authn"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"
)

const spaceSearchMatch = `strpos(search_text,space_search_fold_v1($4))>0
 AND (char_length($4)=1 OR space_search_terms_v1(search_text,2,3) @>
 space_search_terms_v1(space_search_fold_v1($4),LEAST(3,char_length($4)),3))`

const spaceSearchRows = `SELECT 1 AS kind,id,channel_id,id AS parent_id,'' AS title,text AS body,
 author_id,'' AS author_name,created_at_unix_ms AS created_at,sequence
 FROM contents WHERE channel_id=ANY($1::text[]) AND NOT deleted AND `+spaceSearchMatch+`
 UNION ALL
 SELECT 2,id,channel_id,id,title,body,author_id,author_name,created_at*1000,0::bigint
 FROM forum_threads WHERE channel_id=ANY($2::text[]) AND NOT hidden AND `+spaceSearchMatch+`
 UNION ALL
 SELECT 3,r.id,t.channel_id,t.id,t.title,r.body,r.author_id,r.author_name,r.created_at*1000,r.sequence
 FROM (SELECT * FROM forum_replies WHERE NOT hidden AND `+spaceSearchMatch+`) r
 JOIN forum_threads t ON t.id=r.thread_id WHERE t.channel_id=ANY($2::text[]) AND NOT t.hidden
 UNION ALL
 SELECT 4,id,channel_id,id,'' AS title,body,author_id,author_name,created_at*1000,0::bigint
 FROM feed_posts WHERE channel_id=ANY($3::text[]) AND NOT hidden AND `+spaceSearchMatch+`
 UNION ALL
 SELECT 5,r.id,p.channel_id,p.id,'' AS title,r.body,r.author_id,r.author_name,r.created_at*1000,r.sequence
 FROM (SELECT * FROM feed_comments WHERE NOT hidden AND `+spaceSearchMatch+`) r
 JOIN feed_posts p ON p.id=r.post_id WHERE p.channel_id=ANY($3::text[]) AND NOT p.hidden`

const spaceSearchSQL = `SELECT r.channel_id,c.title,r.kind,r.id,r.parent_id,left(r.title,120),r.body,
 r.author_id,COALESCE(NULLIF(a.nickname,''),r.author_name),r.created_at,r.sequence
 FROM (`+spaceSearchRows+`) r JOIN channels c ON c.id=r.channel_id
 LEFT JOIN memberships a ON a.principal_id=r.author_id
 WHERE (NOT $5::boolean OR (r.created_at,r.kind,r.channel_id COLLATE "C",r.id COLLATE "C") <
 ($6::bigint,$7::integer,$8::text COLLATE "C",$9::text COLLATE "C"))
 ORDER BY r.created_at DESC,r.kind DESC,r.channel_id COLLATE "C" DESC,r.id COLLATE "C" DESC LIMIT $10`

func (s *Store) SearchSpace(ctx context.Context, req *pb.SearchSpaceRequest) (*pb.SearchSpaceResponse, error) {
    query := strings.TrimSpace(req.Query)
    if !validForumText(query, 256) || req.Limit < 0 || req.Limit > 50 {
        return nil, status.Error(codes.InvalidArgument, "Нужны текст до 256 байт и страница до 50 результатов")
    }
    limit := int(req.Limit)
    if limit == 0 { limit = 20 }
    binding := searchBinding("space-search-v1", query, authn.Actor(ctx))
    cursor, err := parseSpaceSearchCursor(req.PageToken, binding)
    if err != nil { return nil, err }
    ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
    defer cancel()
    tx, channels, err := s.spaceSearchTransaction(ctx)
    if err != nil { return nil, err }
    defer tx.Rollback(ctx)
    rows, err := tx.Query(ctx, spaceSearchSQL, channels.chat, channels.forum, channels.feed,
        query, req.PageToken != "", cursor.Created, cursor.Kind, cursor.Channel, cursor.ID, limit+1)
    if err != nil { return nil, databaseError(ctx, err) }
    return readSpaceSearchRows(ctx, rows, query, binding, limit)
}

func readSpaceSearchRows(ctx context.Context, rows pgx.Rows, query, binding string, limit int) (*pb.SearchSpaceResponse, error) {
    defer rows.Close()
    result := new(pb.SearchSpaceResponse)
    for rows.Next() {
        item := new(pb.SpaceSearchResult)
        var body string
        if err := rows.Scan(&item.ChannelId,&item.ChannelTitle,&item.Kind,&item.Id,&item.ParentId,
            &item.Title,&body,&item.AuthorId,&item.AuthorName,&item.CreatedAtUnixMs,&item.Sequence); err != nil {
            return nil, databaseError(ctx, err)
        }
        if len(result.Results) == limit {
            result.NextPageToken = spaceSearchToken(result.Results[limit-1], binding)
            break
        }
        item.Snippet = communitySnippet(body, query)
        result.Results = append(result.Results, item)
    }
    return result, databaseErrorOrNil(ctx, rows.Err())
}
