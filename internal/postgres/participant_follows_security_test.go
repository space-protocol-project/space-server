package postgres

import (
 "context"
 "testing"
 pb "github.com/space-protocol-project/space-server/gen/space/v1"
 "google.golang.org/grpc/codes"
)

func TestParticipantFollowsRechecksAuthenticationOnEveryRPC(t *testing.T) {
 f:=newChannelFixture(t);c:=feedSection(t,f,"follow_auth");feedCreate(t,f,f.owner,c.Id,"root")
 api:=pb.NewParticipantFollowServiceClient(f.conn)
 for _,test:=range []struct{name,sql string;code codes.Code}{
  {"scope","UPDATE device_grants SET scopes=ARRAY['chat.write'] WHERE principal_id=$1",codes.PermissionDenied},
  {"grant","UPDATE device_grants SET revoked_at=now() WHERE principal_id=$1",codes.Unauthenticated},
  {"session","UPDATE auth_sessions SET expires_at=now()-interval '1 second' WHERE grant_id IN(SELECT id FROM device_grants WHERE principal_id=$1)",codes.Unauthenticated},
  {"membership","DELETE FROM memberships WHERE principal_id=$1",codes.PermissionDenied},
  {"blocked","UPDATE memberships SET blocked=true WHERE principal_id=$1",codes.PermissionDenied},
 } {
  t.Run(test.name,func(t *testing.T){
   actor,id:=f.enroll(t,true);followSet(t,f,actor,c.Id,f.ownerID,0)
   if _,err:=f.store.pool.Exec(f.ctx,test.sql,id);err!=nil {t.Fatal(err)}
   _,err:=api.GetParticipantFollowStates(actor,&pb.GetParticipantFollowStatesRequest{ChannelId:c.Id,PrincipalIds:[]string{f.ownerID}});requireCode(t,err,test.code)
   _,err=api.SetParticipantFollow(actor,&pb.SetParticipantFollowRequest{PrincipalId:f.ownerID,ExpectedRevision:1});requireCode(t,err,test.code)
   _,err=api.ListFollowingParticipants(actor,&pb.ListFollowingParticipantsRequest{});requireCode(t,err,test.code)
   _,err=api.ListPersonalFeed(actor,&pb.ListPersonalFeedRequest{});requireCode(t,err,test.code)
  })
 }
}

func TestParticipantFollowsReaderMLSAndChangedCursor(t *testing.T) {
 f:=newChannelFixture(t);c:=feedSection(t,f,"follow_reader");feedCreate(t,f,f.owner,c.Id,"one");feedCreate(t,f,f.owner,c.Id,"two")
 actor,id:=f.enroll(t,true)
 if _,err:=f.store.pool.Exec(f.ctx,"UPDATE device_grants SET scopes=ARRAY['chat.read'] WHERE principal_id=$1",id);err!=nil {t.Fatal(err)}
 if _,err:=f.store.pool.Exec(f.ctx,"UPDATE memberships SET role='reader' WHERE principal_id=$1",id);err!=nil {t.Fatal(err)}
 f.acl(t,c,principalRule(id,permissions(true,true,false,false)))
 followSet(t,f,actor,c.Id,f.ownerID,0)
 api:=pb.NewParticipantFollowServiceClient(f.conn)
 first,err:=api.ListPersonalFeed(actor,&pb.ListPersonalFeedRequest{Limit:1});if err!=nil||first.NextPageToken=="" {t.Fatal(first,err)}
 _,err=api.SetParticipantFollow(actor,&pb.SetParticipantFollowRequest{PrincipalId:f.ownerID,ExpectedRevision:1});if err!=nil {t.Fatal(err)}
 _,err=api.ListPersonalFeed(actor,&pb.ListPersonalFeedRequest{PageToken:first.NextPageToken});requireCode(t,err,codes.Aborted)
 followSet(t,f,actor,c.Id,f.ownerID,2)
 if _,err=f.store.pool.Exec(f.ctx,"UPDATE channels SET mls_group_id=decode(repeat('ab',16),'hex') WHERE id=$1",c.Id);err!=nil {t.Fatal(err)}
 page,err:=api.ListPersonalFeed(actor,&pb.ListPersonalFeedRequest{});if err!=nil||len(page.Entries)!=0 {t.Fatal(page,err)}
 for _,caller:=range []context.Context{actor,f.owner} {
  _,err=api.GetParticipantFollowStates(caller,&pb.GetParticipantFollowStatesRequest{ChannelId:c.Id,PrincipalIds:[]string{f.ownerID}});requireCode(t,err,codes.NotFound)
 }
}

func TestFollowingNamesRespectProfileVisibility(t *testing.T) {
 f:=newChannelFixture(t);c:=feedSection(t,f,"follow_profiles");feedPolicy(t,f,c,feedRole("member",true,true))
 author,authorID:=f.enroll(t,true);reader,_:=f.enroll(t,true)
 feedCreate(t,f,author,c.Id,"author");feedCreate(t,f,f.owner,c.Id,"owner")
 api:=pb.NewParticipantFollowServiceClient(f.conn)
 followSet(t,f,reader,c.Id,authorID,0);followSet(t,f,reader,c.Id,f.ownerID,1)
 if _,err:=f.store.pool.Exec(f.ctx,"UPDATE memberships SET nickname='Новое имя' WHERE principal_id=$1",authorID);err!=nil {t.Fatal(err)}
 first,err:=api.ListFollowingParticipants(reader,&pb.ListFollowingParticipantsRequest{Limit:1});if err!=nil||len(first.Participants)!=1||first.NextPageToken=="" {t.Fatal(first,err)}
 second,err:=api.ListFollowingParticipants(reader,&pb.ListFollowingParticipantsRequest{PageToken:first.NextPageToken,Limit:1});if err!=nil||len(second.Participants)!=1||second.NextPageToken!=""||first.Participants[0].PrincipalId>=second.Participants[0].PrincipalId {t.Fatal(second,err)}
 people:=append(first.Participants,second.Participants...)
 for _,person:=range people {if person.PrincipalId==authorID&&person.DisplayName!="Новое имя" {t.Fatal(person)}}
 f.acl(t,c,principalRule(authorID,permissions(false,false,false,false)))
 page,err:=api.ListFollowingParticipants(reader,&pb.ListFollowingParticipantsRequest{});if err!=nil {t.Fatal(err)}
 for _,person:=range page.Participants {if person.PrincipalId==authorID&&(person.Available||person.DisplayName!="") {t.Fatal("Раскрыт недоступный текущий профиль",person)}}
 _,err=api.SetParticipantFollow(reader,&pb.SetParticipantFollowRequest{ChannelId:c.Id,PrincipalId:authorID,Following:true,ExpectedRevision:2});requireCode(t,err,codes.NotFound)
}
