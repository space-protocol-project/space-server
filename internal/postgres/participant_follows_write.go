package postgres

import (
 "context"
 "time"
 "github.com/jackc/pgx/v5"
 pb "github.com/space-protocol-project/space-server/gen/space/v1"
 "github.com/space-protocol-project/space-server/internal/authn"
 "google.golang.org/grpc/codes"
 "google.golang.org/grpc/status"
)

func (s *Store) GetParticipantFollowStates(ctx context.Context,req *pb.GetParticipantFollowStatesRequest) (*pb.GetParticipantFollowStatesResponse,error) {
 if len(req.PrincipalIds)==0 || len(req.PrincipalIds)>50 {return nil,status.Error(codes.InvalidArgument,"Нужно 1–50 участников")}
 seen:=map[string]bool{}
 for _,id:=range req.PrincipalIds {if !validFollowID(id)||seen[id]{return nil,status.Error(codes.InvalidArgument,"Некорректный или повторный участник")};seen[id]=true}
 ctx,cancel:=context.WithTimeout(ctx,3*time.Second);defer cancel()
 tx,channels,revision,err:=s.followTransaction(ctx);if err!=nil{return nil,err};defer tx.Rollback(ctx)
 if err=followChannel(channels,req.ChannelId);err!=nil{return nil,err}
 names,err:=visibleFollowNames(ctx,tx,[]string{req.ChannelId},req.PrincipalIds);if err!=nil{return nil,err}
 out:=&pb.GetParticipantFollowStatesResponse{Revision:revision}
 for _,id:=range req.PrincipalIds {
  if _,ok:=names[id];!ok||id==authn.Actor(ctx){continue}
  state,err:=followState(ctx,tx,id);if err!=nil{return nil,err};out.States=append(out.States,state)
 }
 return out,nil
}

func requireFollowTarget(ctx context.Context,tx pgx.Tx,channels []string,req *pb.SetParticipantFollowRequest) error {
 if !req.Following {return nil}
 if err:=followChannel(channels,req.ChannelId);err!=nil{return err}
 names,err:=visibleFollowNames(ctx,tx,[]string{req.ChannelId},[]string{req.PrincipalId})
 if err!=nil{return err}
 if _,ok:=names[req.PrincipalId];!ok{return status.Error(codes.NotFound,"Участник недоступен")}
 return nil
}

func applyParticipantFollow(ctx context.Context,tx pgx.Tx,req *pb.SetParticipantFollowRequest) error {
 if req.Following {
  var count int
  if err:=tx.QueryRow(ctx,"SELECT count(*) FROM participant_follows WHERE principal_id=$1",authn.Actor(ctx)).Scan(&count);err!=nil{return databaseError(ctx,err)}
  if count>=1000{return status.Error(codes.ResourceExhausted,"Не более 1000 подписок")}
  if _,err:=tx.Exec(ctx,"INSERT INTO participant_follows(principal_id,target_id) VALUES($1,$2)",authn.Actor(ctx),req.PrincipalId);err!=nil{return databaseError(ctx,err)}
 } else {
  if _,err:=tx.Exec(ctx,"DELETE FROM participant_follows WHERE principal_id=$1 AND target_id=$2",authn.Actor(ctx),req.PrincipalId);err!=nil{return databaseError(ctx,err)}
 }
 _,err:=tx.Exec(ctx,`INSERT INTO participant_follow_versions(principal_id,revision) VALUES($1,1)
 ON CONFLICT(principal_id) DO UPDATE SET revision=participant_follow_versions.revision+1`,authn.Actor(ctx))
 return databaseErrorOrNil(ctx,err)
}

func (s *Store) SetParticipantFollow(ctx context.Context,req *pb.SetParticipantFollowRequest) (*pb.SetParticipantFollowResponse,error) {
 if !validFollowID(req.PrincipalId)||req.PrincipalId==authn.Actor(ctx)||req.ExpectedRevision<0 {return nil,status.Error(codes.InvalidArgument,"Некорректный участник или ревизия")}
 ctx,cancel:=context.WithTimeout(ctx,3*time.Second);defer cancel()
 tx,channels,revision,err:=s.followTransaction(ctx);if err!=nil{return nil,err};defer tx.Rollback(ctx)
 if err=requireFollowTarget(ctx,tx,channels,req);err!=nil{return nil,err}
 current,err:=followState(ctx,tx,req.PrincipalId);if err!=nil{return nil,err}
 if current.Following==req.Following {return &pb.SetParticipantFollowResponse{State:current,Revision:revision},nil}
 if revision!=req.ExpectedRevision{return nil,status.Error(codes.Aborted,"Подписки изменились; обновите список")}
 if err=applyParticipantFollow(ctx,tx,req);err!=nil{return nil,err}
 current.Following=req.Following
 return &pb.SetParticipantFollowResponse{State:current,Revision:revision+1},databaseErrorOrNil(ctx,tx.Commit(ctx))
}
