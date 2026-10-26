package postgres

import (
 "context"
 "time"
 pb "github.com/space-protocol-project/space-server/gen/space/v1"
 "github.com/space-protocol-project/space-server/internal/authn"
)

func (s *Store) ListFollowingParticipants(ctx context.Context,req *pb.ListFollowingParticipantsRequest) (*pb.ListFollowingParticipantsResponse,error) {
 limit,err:=followLimit(req.Limit);if err!=nil{return nil,err}
 ctx,cancel:=context.WithTimeout(ctx,3*time.Second);defer cancel()
 tx,channels,revision,err:=s.followTransaction(ctx);if err!=nil{return nil,err};defer tx.Rollback(ctx)
 binding:=searchBinding("following-v1","",authn.Actor(ctx))
 cursor,err:=parseFollowCursor(req.PageToken,binding,revision,false);if err!=nil{return nil,err}
 rows,err:=tx.Query(ctx,`SELECT target_id FROM participant_follows WHERE principal_id=$1 AND target_id COLLATE "C">$2 COLLATE "C" ORDER BY target_id COLLATE "C" LIMIT $3`,authn.Actor(ctx),cursor.ID,limit+1)
 if err!=nil{return nil,databaseError(ctx,err)}
 defer rows.Close()
 ids:=[]string{}
 for rows.Next(){var id string;if err=rows.Scan(&id);err!=nil{return nil,databaseError(ctx,err)};ids=append(ids,id)}
 err=rows.Err();rows.Close();if err!=nil{return nil,databaseError(ctx,err)}
 out:=&pb.ListFollowingParticipantsResponse{Revision:revision}
 if len(ids)>limit {ids=ids[:limit];out.NextPageToken=encodeFollowCursor(followCursor{1,binding,revision,0,"",ids[limit-1]})}
 names,err:=visibleFollowNames(ctx,tx,channels,ids);if err!=nil{return nil,err}
 for _,id:=range ids {name,visible:=names[id];out.Participants=append(out.Participants,&pb.FollowingParticipant{PrincipalId:id,DisplayName:name,Available:visible})}
 return out,nil
}
