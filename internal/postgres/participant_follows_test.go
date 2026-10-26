package postgres

import (
 "context"
 "encoding/base64"
 "strings"
 "sync"
 "testing"
 pb "github.com/space-protocol-project/space-server/gen/space/v1"
 "google.golang.org/grpc/codes"
 "google.golang.org/grpc/status"
)

func followSet(t *testing.T,f *channelFixture,actor context.Context,channel,target string,revision int64) int64 {
 t.Helper()
 out,err:=pb.NewParticipantFollowServiceClient(f.conn).SetParticipantFollow(actor,&pb.SetParticipantFollowRequest{ChannelId:channel,PrincipalId:target,Following:true,ExpectedRevision:revision})
 if err!=nil {t.Fatal(err)}
 if !out.State.Following||out.State.PrincipalId!=target {t.Fatal(out)}
 return out.Revision
}

func TestParticipantFollowsPrivateRevisionAndRetry(t *testing.T) {
 f:=newChannelFixture(t);c:=feedSection(t,f,"follows");feedCreate(t,f,f.owner,c.Id,"owner")
 member,id:=f.enroll(t,true);other,_:=f.enroll(t,true)
 api:=pb.NewParticipantFollowServiceClient(f.conn)
 req:=&pb.SetParticipantFollowRequest{ChannelId:c.Id,PrincipalId:f.ownerID,Following:true}
 made,err:=api.SetParticipantFollow(member,req);if err!=nil||made.GetRevision()!=1 {t.Fatal(made,err)}
 again,err:=api.SetParticipantFollow(member,req);if err!=nil||again.GetRevision()!=1 {t.Fatal(again,err)}
 own,err:=api.ListFollowingParticipants(member,&pb.ListFollowingParticipantsRequest{});if err!=nil||len(own.Participants)!=1||!own.Participants[0].Available {t.Fatal(own,err)}
 empty,err:=api.ListFollowingParticipants(other,&pb.ListFollowingParticipantsRequest{});if err!=nil||len(empty.Participants)!=0||empty.Revision!=0 {t.Fatal(empty,err)}
 _,err=api.SetParticipantFollow(member,&pb.SetParticipantFollowRequest{PrincipalId:f.ownerID});requireCode(t,err,codes.Aborted)
 removed,err:=api.SetParticipantFollow(member,&pb.SetParticipantFollowRequest{PrincipalId:f.ownerID,ExpectedRevision:1});if err!=nil||removed.Revision!=2||removed.State.Following {t.Fatal(removed,err)}
 // Запоздавший повтор старой подписки не воскрешает отменённую подписку.
 _,err=api.SetParticipantFollow(member,req);requireCode(t,err,codes.Aborted)
 _,err=api.SetParticipantFollow(member,&pb.SetParticipantFollowRequest{ChannelId:c.Id,PrincipalId:id,Following:true});requireCode(t,err,codes.InvalidArgument)
 _,err=api.ListPersonalFeed(f.ctx,&pb.ListPersonalFeedRequest{});requireCode(t,err,codes.Unauthenticated)
}

func TestPersonalFeedACLBeforeLimitAndRevocation(t *testing.T) {
 f:=newChannelFixture(t);a:=feedSection(t,f,"personal_a");b:=feedSection(t,f,"personal_b")
 old:=feedCreate(t,f,f.owner,a.Id,"a");hidden:=feedCreate(t,f,f.owner,b.Id,"b")
 member,id:=f.enroll(t,true);api:=pb.NewParticipantFollowServiceClient(f.conn)
 followSet(t,f,member,a.Id,f.ownerID,0)
 if _,err:=f.store.pool.Exec(f.ctx,"UPDATE feed_posts SET created_at=CASE WHEN id=$1 THEN 1 ELSE 2 END",old.Id);err!=nil {t.Fatal(err)}
 f.acl(t,b,principalRule(id,permissions(true,false,false,true)))
 page,err:=api.ListPersonalFeed(member,&pb.ListPersonalFeedRequest{Limit:1})
 if err!=nil||len(page.Entries)!=1||page.Entries[0].Post.Id!=old.Id||page.NextPageToken!="" {t.Fatal(page,err,hidden.Id)}
 f.acl(t,a,principalRule(id,permissions(false,false,false,false)))
 page,err=api.ListPersonalFeed(member,&pb.ListPersonalFeedRequest{});if err!=nil||len(page.Entries)!=0 {t.Fatal(page,err)}
 list,err:=api.ListFollowingParticipants(member,&pb.ListFollowingParticipantsRequest{});if err!=nil||len(list.Participants)!=1||list.Participants[0].Available||list.Participants[0].DisplayName!="" {t.Fatal(list,err)}
 _,err=api.SetParticipantFollow(member,&pb.SetParticipantFollowRequest{ChannelId:a.Id,PrincipalId:f.ownerID,Following:true,ExpectedRevision:1});requireCode(t,err,codes.NotFound)
 // Отписка разрешена даже после утраты доступа к автору.
 _,err=api.SetParticipantFollow(member,&pb.SetParticipantFollowRequest{PrincipalId:f.ownerID,ExpectedRevision:1});if err!=nil {t.Fatal(err)}
}

func TestPersonalFeedPagesQuotesHiddenAndArchive(t *testing.T) {
 f:=newChannelFixture(t);c:=feedSection(t,f,"personal_pages");original:=feedCreate(t,f,f.owner,c.Id,"root")
 feed:=pb.NewFeedServiceClient(f.conn)
 quote,err:=feed.CreateFeedPost(f.owner,&pb.CreateFeedPostRequest{ChannelId:c.Id,RepostOf:original.Id,Text:"Цитата",IdempotencyKey:"quote"});if err!=nil {t.Fatal(err)}
 member,memberID:=f.enroll(t,true);api:=pb.NewParticipantFollowServiceClient(f.conn);followSet(t,f,member,c.Id,f.ownerID,0)
 if _,err=f.store.pool.Exec(f.ctx,"UPDATE feed_posts SET created_at=CASE WHEN id=$1 THEN 2 ELSE 1 END",quote.Post.Id);err!=nil {t.Fatal(err)}
 page,err:=api.ListPersonalFeed(member,&pb.ListPersonalFeedRequest{Limit:1});if err!=nil||len(page.Entries)!=1||page.NextPageToken==""||page.Entries[0].Post.GetQuotedPost().GetId()!=original.Id {t.Fatal(page,err)}
 var readMarks int
 if err=f.store.pool.QueryRow(f.ctx,"SELECT count(*) FROM feed_read_state WHERE principal_id=$1",memberID).Scan(&readMarks);err!=nil||readMarks!=0 {t.Fatal("Личная лента изменила отметки чтения",readMarks,err)}
 next,err:=api.ListPersonalFeed(member,&pb.ListPersonalFeedRequest{PageToken:page.NextPageToken});if err!=nil||len(next.Entries)!=1||next.Entries[0].Post.Id!=original.Id {t.Fatal(next,err)}
 _,err=api.ListFollowingParticipants(member,&pb.ListFollowingParticipantsRequest{PageToken:page.NextPageToken});requireCode(t,err,codes.InvalidArgument)
 stranger,_:=f.enroll(t,true);_,err=api.ListPersonalFeed(stranger,&pb.ListPersonalFeedRequest{PageToken:page.NextPageToken});requireCode(t,err,codes.InvalidArgument)
 _,err=feed.ModerateFeedPost(f.owner,&pb.ModerateFeedPostRequest{ChannelId:c.Id,PostId:original.Id,Hidden:true,Reason:"Проверка",ExpectedRevision:original.Revision});if err!=nil {t.Fatal(err)}
 page,err=api.ListPersonalFeed(member,&pb.ListPersonalFeedRequest{});if err!=nil||len(page.Entries)!=1||page.Entries[0].Post.QuotedPost!=nil {t.Fatal(page,err)}
 if _,err=f.store.pool.Exec(f.ctx,"UPDATE channels SET archived=true WHERE id=$1",c.Id);err!=nil {t.Fatal(err)}
 page,err=api.ListPersonalFeed(member,&pb.ListPersonalFeedRequest{});if err!=nil||len(page.Entries)!=0 {t.Fatal(page,err)}
}


func TestParticipantFollowsCommentAuthorBlockedAndConcurrentCAS(t *testing.T) {
 f:=newChannelFixture(t);c:=feedSection(t,f,"follow_comments");root:=feedCreate(t,f,f.owner,c.Id,"root")
 member,id:=f.enroll(t,true);reader,_:=f.enroll(t,true);feedPolicy(t,f,c,feedRole("member",false,true))
 _,err:=pb.NewFeedServiceClient(f.conn).CommentFeedPost(member,&pb.CommentFeedPostRequest{ChannelId:c.Id,PostId:root.Id,Text:"Ответ",IdempotencyKey:"comment"});if err!=nil {t.Fatal(err)}
 api:=pb.NewParticipantFollowServiceClient(f.conn)
 states,err:=api.GetParticipantFollowStates(reader,&pb.GetParticipantFollowStatesRequest{ChannelId:c.Id,PrincipalIds:[]string{id,f.ownerID,"missing"}});if err!=nil||len(states.States)!=2 {t.Fatal(states,err)}
 // Два устройства не могут применить противоположные изменения к старой ревизии.
 var wg sync.WaitGroup;results:=make(chan error,2)
 for _,target:=range []string{id,f.ownerID} {wg.Add(1);go func(target string){defer wg.Done();_,err:=api.SetParticipantFollow(reader,&pb.SetParticipantFollowRequest{ChannelId:c.Id,PrincipalId:target,Following:true});results<-err}(target)}
 wg.Wait();close(results);success,conflict:=0,0
 for err:=range results {switch status.Code(err) {case codes.OK:success++;case codes.Aborted:conflict++;default:t.Fatal(err)}}
 if success!=1||conflict!=1 {t.Fatal(success,conflict)}
 if _,err=f.store.pool.Exec(f.ctx,"UPDATE memberships SET blocked=true WHERE principal_id=$1",id);err!=nil {t.Fatal(err)}
 _,err=api.SetParticipantFollow(reader,&pb.SetParticipantFollowRequest{ChannelId:c.Id,PrincipalId:id,Following:true,ExpectedRevision:1});requireCode(t,err,codes.NotFound)
 _,err=api.ListPersonalFeed(member,&pb.ListPersonalFeedRequest{});requireCode(t,err,codes.PermissionDenied)
}

func TestFollowCursorValidation(t *testing.T) {
 good:=followCursor{1,"binding",3,1,"feed","post"}
 token:=encodeFollowCursor(good)
 if _,err:=parseFollowCursor(token,"binding",3,true);err!=nil {t.Fatal(err)}
 _,err:=parseFollowCursor(token,"binding",4,true);requireCode(t,err,codes.Aborted)
 for _,bad:=range []string{"!",strings.Repeat("a",1025),base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"b":"binding","r":3,"t":1,"c":"feed","i":"post","unknown":1}`)),base64.RawURLEncoding.EncodeToString([]byte(`{} {}`))} {
  _,err=parseFollowCursor(bad,"binding",3,true);requireCode(t,err,codes.InvalidArgument)
 }
 _,err=parseFollowCursor(token,"other",3,true);requireCode(t,err,codes.InvalidArgument)
}
