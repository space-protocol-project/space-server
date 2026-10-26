package postgres

import (
 "context"
 "encoding/json"
 "time"
 "github.com/jackc/pgx/v5"
 pb "github.com/space-protocol-project/space-server/gen/space/v1"
 "github.com/space-protocol-project/space-server/internal/authn"
 "google.golang.org/grpc/codes"
 "google.golang.org/grpc/status"
 "google.golang.org/protobuf/encoding/protojson"
 "google.golang.org/protobuf/proto"
)

const userExportLimit=5000
const userExportBytes=8*1024*1024

// У каждого подзапроса собственный author_id; родитель проверяется до LIMIT.
const userExportRows=`SELECT 1 AS kind,id,channel_id,id AS parent_id,'' AS title,text AS body,
 created_at_unix_ms AS created_at,revision,attachments,'' AS repost_of
 FROM contents WHERE channel_id=ANY($1::text[]) AND author_id=$4 AND NOT deleted
 UNION ALL SELECT 2,id,channel_id,id,title,body,created_at*1000,revision,attachments,''
 FROM forum_threads WHERE channel_id=ANY($2::text[]) AND author_id=$4 AND NOT hidden
 UNION ALL SELECT 3,r.id,r.channel_id,r.thread_id,'',r.body,r.created_at*1000,r.revision,r.attachments,''
 FROM forum_replies r JOIN forum_threads t ON t.id=r.thread_id
 WHERE r.channel_id=ANY($2::text[]) AND r.author_id=$4 AND NOT r.hidden AND NOT t.hidden
 UNION ALL SELECT 4,id,channel_id,id,'',body,created_at*1000,revision,attachments,COALESCE(repost_of,'')
 FROM feed_posts WHERE channel_id=ANY($3::text[]) AND author_id=$4 AND NOT hidden
 UNION ALL SELECT 5,r.id,r.channel_id,r.post_id,'',r.body,r.created_at*1000,r.revision,r.attachments,''
 FROM feed_comments r JOIN feed_posts p ON p.id=r.post_id
 WHERE r.channel_id=ANY($3::text[]) AND r.author_id=$4 AND NOT r.hidden AND NOT p.hidden`

const userExportSQL=`SELECT r.kind,r.id,r.channel_id,c.title,r.parent_id,r.title,r.body,r.created_at,r.revision,r.attachments,r.repost_of
 FROM (`+userExportRows+`) r JOIN channels c ON c.id=r.channel_id
 WHERE ($5='' OR r.channel_id=$5) AND r.created_at>=$6 AND ($7::bigint=0 OR r.created_at<$7)
 ORDER BY r.created_at,r.kind,r.channel_id COLLATE "C",r.id COLLATE "C" LIMIT 5001`

func validUserExport(req *pb.ExportUserContentRequest) bool {
 if req.ChannelId!=""&&!channelIDPattern.MatchString(req.ChannelId){return false}
 if req.CreatedAfterUnixMs<0||req.CreatedBeforeUnixMs<0 {return false}
 return req.CreatedBeforeUnixMs==0||req.CreatedBeforeUnixMs>req.CreatedAfterUnixMs
}

func exportAttachments(data []byte) ([]*pb.UserExportAttachment,error) {
 var assets []*pb.Asset
 if err:=json.Unmarshal(data,&assets);err!=nil{return nil,err}
 out:=make([]*pb.UserExportAttachment,0,len(assets))
 for _,asset:=range assets {out=append(out,&pb.UserExportAttachment{Id:asset.Id,Name:asset.Name,MimeType:asset.MediaType,ByteSize:asset.ByteSize,Sha256:asset.Sha256})}
 return out,nil
}

func readUserExport(ctx context.Context,rows pgx.Rows,out *pb.ExportUserContentResponse) error {
 defer rows.Close()
 size:=0
 for rows.Next() {
  entry:=new(pb.UserExportEntry);var assets []byte
  err:=rows.Scan(&entry.Kind,&entry.Id,&entry.ChannelId,&entry.ChannelTitle,&entry.ParentId,&entry.Title,&entry.Text,&entry.CreatedAtUnixMs,&entry.Revision,&assets,&entry.RepostOf)
  if err!=nil{return databaseError(ctx,err)}
  entry.Attachments,err=exportAttachments(assets);if err!=nil{return databaseError(ctx,err)}
  size+=proto.Size(entry)+16
  if len(out.Entries)==userExportLimit||size>userExportBytes{return status.Error(codes.ResourceExhausted,"Выберите меньший период: максимум 5000 записей и 8 МиБ")}
  out.Entries=append(out.Entries,entry)
 }
 if err:=rows.Err();err!=nil{return databaseError(ctx,err)}
 encoded,err:=protojson.Marshal(out);if err!=nil{return databaseError(ctx,err)}
 if len(encoded)>userExportBytes{return status.Error(codes.ResourceExhausted,"Выберите меньший период: максимум 8 МиБ JSON")}
 return nil
}

func (s *Store) ExportUserContent(ctx context.Context,req *pb.ExportUserContentRequest) (*pb.ExportUserContentResponse,error) {
 if !validUserExport(req){return nil,status.Error(codes.InvalidArgument,"Некорректный раздел или период экспорта")}
 ctx,cancel:=context.WithTimeout(ctx,5*time.Second);defer cancel()
 tx,channels,err:=s.spaceSearchTransaction(ctx);if err!=nil{return nil,err};defer tx.Rollback(ctx)
 if req.ChannelId!="" {
  ids:=append(append(append([]string{},channels.chat...),channels.forum...),channels.feed...)
  if err=followChannel(ids,req.ChannelId);err!=nil{return nil,err}
 }
 out:=&pb.ExportUserContentResponse{FormatVersion:1,PrincipalId:authn.Actor(ctx),GeneratedAtUnixMs:time.Now().UnixMilli()}
 if err=tx.QueryRow(ctx,"SELECT server_id FROM server_state WHERE singleton=true").Scan(&out.ServerId);err!=nil{return nil,databaseError(ctx,err)}
 rows,err:=tx.Query(ctx,userExportSQL,channels.chat,channels.forum,channels.feed,authn.Actor(ctx),req.ChannelId,req.CreatedAfterUnixMs,req.CreatedBeforeUnixMs)
 if err!=nil{return nil,databaseError(ctx,err)}
 if err=readUserExport(ctx,rows,out);err!=nil{return nil,err}
 return out,nil
}
