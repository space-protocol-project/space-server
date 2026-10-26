package postgres

import (
 "context"
 "time"
 "github.com/jackc/pgx/v5"
 pb "github.com/space-protocol-project/space-server/gen/space/v1"
 "github.com/space-protocol-project/space-server/internal/authn"
)

func personalFeedNames(ctx context.Context,tx pgx.Tx,channels []string) (map[string]string,error) {
 rows,err:=tx.Query(ctx,"SELECT id,title FROM channels WHERE id=ANY($1)",channels)
 if err!=nil{return nil,databaseError(ctx,err)}
 defer rows.Close()
 names:=map[string]string{}
 for rows.Next(){var id,title string;if err=rows.Scan(&id,&title);err!=nil{return nil,databaseError(ctx,err)};names[id]=title}
 return names,databaseErrorOrNil(ctx,rows.Err())
}

func lockPersonalFeedAuthors(ctx context.Context,tx pgx.Tx) error {
 rows,err:=tx.Query(ctx,`SELECT principal_id FROM memberships WHERE principal_id IN
 (SELECT target_id FROM participant_follows WHERE principal_id=$1) ORDER BY principal_id COLLATE "C" FOR SHARE`,authn.Actor(ctx))
 if err!=nil{return databaseError(ctx,err)}
 defer rows.Close()
 for rows.Next(){var id string;if err=rows.Scan(&id);err!=nil{return databaseError(ctx,err)}}
 return databaseErrorOrNil(ctx,rows.Err())
}

func (s *Store) ListPersonalFeed(ctx context.Context,req *pb.ListPersonalFeedRequest) (*pb.ListPersonalFeedResponse,error) {
 limit,err:=followLimit(req.Limit);if err!=nil{return nil,err}
 ctx,cancel:=context.WithTimeout(ctx,3*time.Second);defer cancel()
 tx,channels,revision,err:=s.followTransaction(ctx);if err!=nil{return nil,err};defer tx.Rollback(ctx)
 binding:=searchBinding("personal-feed-v1","",authn.Actor(ctx))
 cursor,err:=parseFollowCursor(req.PageToken,binding,revision,true);if err!=nil{return nil,err}
 if err=lockPersonalFeedAuthors(ctx,tx);err!=nil{return nil,err}
 names,err:=personalFeedNames(ctx,tx,channels);if err!=nil{return nil,err}
 rows,err:=tx.Query(ctx,`SELECT `+feedPostColumns+` FROM feed_posts p WHERE channel_id=ANY($1::text[]) AND NOT hidden
 AND EXISTS(SELECT 1 FROM participant_follows f WHERE f.principal_id=$2 AND f.target_id=p.author_id)
 AND EXISTS(SELECT 1 FROM memberships m WHERE m.principal_id=p.author_id AND NOT m.blocked)
 AND (NOT $3 OR (created_at,channel_id COLLATE "C",id COLLATE "C")<($4::bigint,$5::text COLLATE "C",$6::text COLLATE "C"))
 ORDER BY created_at DESC,channel_id COLLATE "C" DESC,id COLLATE "C" DESC LIMIT $7`,channels,authn.Actor(ctx),req.PageToken!="",cursor.Created,cursor.Channel,cursor.ID,limit+1)
 if err!=nil{return nil,databaseError(ctx,err)}
 defer rows.Close()
 out:=&pb.ListPersonalFeedResponse{Revision:revision}
 posts:=[]*pb.FeedPost{}
 for rows.Next(){
  post,err:=scanFeedPost(rows);if err!=nil{return nil,databaseError(ctx,err)}
  if len(posts)==limit {last:=posts[limit-1];out.NextPageToken=encodeFollowCursor(followCursor{1,binding,revision,last.CreatedAtUnix,last.ChannelId,last.Id});break}
  posts=append(posts,post);out.Entries=append(out.Entries,&pb.PersonalFeedEntry{ChannelTitle:names[post.ChannelId],Post:post})
 }
 err=rows.Err();rows.Close();if err!=nil{return nil,databaseError(ctx,err)}
 if err=feedReadStates(ctx,tx,posts);err!=nil{return nil,err}
 return out,nil
}
