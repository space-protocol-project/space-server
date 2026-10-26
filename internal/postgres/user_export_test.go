package postgres

import (
 "strings"
 "testing"
 pb "github.com/space-protocol-project/space-server/gen/space/v1"
 "google.golang.org/grpc/codes"
 "google.golang.org/protobuf/encoding/protojson"
)

func TestUserExportOwnContentAndReferences(t *testing.T) {
 f:=newChannelFixture(t);sections:=spaceSearchSeed(t,f)
 actor,id:=f.enroll(t,true);feedPolicy(t,f,sections[2],feedRole("member",true,true))
 chat,err:=f.contents.CreateContent(actor,&pb.CreateContentRequest{ChannelId:sections[0].Id,Text:"Мой текст",IdempotencyKey:"mine"});if err!=nil {t.Fatal(err)}
 feed:=pb.NewFeedServiceClient(f.conn)
 original:=feedCreate(t,f,f.owner,sections[2].Id,"Чужой секретный оригинал")
 quote,err:=feed.CreateFeedPost(actor,&pb.CreateFeedPostRequest{ChannelId:sections[2].Id,Text:"Моя цитата",RepostOf:original.Id,IdempotencyKey:"quote"});if err!=nil {t.Fatal(err)}
 api:=pb.NewUserExportServiceClient(f.conn)
 out,err:=api.ExportUserContent(actor,&pb.ExportUserContentRequest{});if err!=nil||out.PrincipalId!=id||out.FormatVersion!=1||len(out.Entries)!=2 {t.Fatal(out,err)}
 found:=map[string]*pb.UserExportEntry{};for _,entry:=range out.Entries {found[entry.Id]=entry}
 if found[chat.Content.Id]==nil||found[quote.Post.Id].RepostOf!=original.Id {t.Fatal(out)}
 encoded,err:=protojson.Marshal(out);if err!=nil||strings.Contains(string(encoded),"Чужой секретный оригинал") {t.Fatal(string(encoded),err)}
 owner,err:=api.ExportUserContent(f.owner,&pb.ExportUserContentRequest{});if err!=nil||len(owner.Entries)!=6 {t.Fatal(owner,err)}
 // Включительная нижняя и исключительная верхняя границы, фильтр раздела.
 one,err:=api.ExportUserContent(actor,&pb.ExportUserContentRequest{ChannelId:sections[0].Id,CreatedAfterUnixMs:chat.Content.CreatedAtUnixMs,CreatedBeforeUnixMs:chat.Content.CreatedAtUnixMs+1})
 if err!=nil||len(one.Entries)!=1 {t.Fatal(one,err)}
}

func TestUserExportACLHiddenParentsAndMLS(t *testing.T) {
 f:=newChannelFixture(t);sections:=spaceSearchSeed(t,f);api:=pb.NewUserExportServiceClient(f.conn)
 // Собственные ответы тоже скрываются, если недоступен родитель.
 if _,err:=f.store.pool.Exec(f.ctx,"UPDATE forum_threads SET hidden=true");err!=nil {t.Fatal(err)}
 if _,err:=f.store.pool.Exec(f.ctx,"UPDATE feed_posts SET hidden=true");err!=nil {t.Fatal(err)}
 out,err:=api.ExportUserContent(f.owner,&pb.ExportUserContentRequest{});if err!=nil||len(out.Entries)!=1 {t.Fatal(out,err)}
 if _,err=f.store.pool.Exec(f.ctx,"UPDATE channels SET mls_group_id=decode(repeat('ab',16),'hex') WHERE id=$1",sections[0].Id);err!=nil {t.Fatal(err)}
 out,err=api.ExportUserContent(f.owner,&pb.ExportUserContentRequest{});if err!=nil||len(out.Entries)!=0 {t.Fatal(out,err)}
 _,err=api.ExportUserContent(f.owner,&pb.ExportUserContentRequest{ChannelId:sections[0].Id});requireCode(t,err,codes.NotFound)
 actor,id:=f.enroll(t,true);f.acl(t,sections[2],principalRule(id,permissions(true,false,false,true)))
 _,err=api.ExportUserContent(actor,&pb.ExportUserContentRequest{ChannelId:sections[2].Id});requireCode(t,err,codes.NotFound)
}

func TestUserExportReaderRevocationsAndValidation(t *testing.T) {
 f:=newChannelFixture(t);c:=f.create(t,"export_auth");api:=pb.NewUserExportServiceClient(f.conn)
 for _,test:=range []struct{sql string;code codes.Code}{
  {"UPDATE device_grants SET scopes=ARRAY['chat.write'] WHERE principal_id=$1",codes.PermissionDenied},
  {"UPDATE device_grants SET revoked_at=now() WHERE principal_id=$1",codes.Unauthenticated},
  {"UPDATE auth_sessions SET expires_at=now()-interval '1 second' WHERE grant_id IN(SELECT id FROM device_grants WHERE principal_id=$1)",codes.Unauthenticated},
  {"UPDATE memberships SET blocked=true WHERE principal_id=$1",codes.PermissionDenied},
 } {
  actor,id:=f.enroll(t,true)
  if _,err:=f.store.pool.Exec(f.ctx,test.sql,id);err!=nil {t.Fatal(err)}
  _,err:=api.ExportUserContent(actor,&pb.ExportUserContentRequest{});requireCode(t,err,test.code)
 }
 actor,id:=f.enroll(t,true)
 if _,err:=f.store.pool.Exec(f.ctx,"UPDATE device_grants SET scopes=ARRAY['chat.read'] WHERE principal_id=$1",id);err!=nil {t.Fatal(err)}
 out,err:=api.ExportUserContent(actor,&pb.ExportUserContentRequest{ChannelId:c.Id});if err!=nil||len(out.Entries)!=0 {t.Fatal(out,err)}
 for _,req:=range []*pb.ExportUserContentRequest{{CreatedAfterUnixMs:-1},{CreatedAfterUnixMs:2,CreatedBeforeUnixMs:1},{ChannelId:"bad space"}} {
  _,err=api.ExportUserContent(actor,req);requireCode(t,err,codes.InvalidArgument)
 }
 _,err=api.ExportUserContent(f.ctx,&pb.ExportUserContentRequest{});requireCode(t,err,codes.Unauthenticated)
}

func TestUserExportDoesNotSilentlyTruncate(t *testing.T) {
 f:=newChannelFixture(t);c:=f.create(t,"export_limit")
 _,err:=f.store.pool.Exec(f.ctx,`INSERT INTO contents(channel_id,sequence,id,text,idempotency_key,author_id,created_at_unix_ms)
 SELECT $1,i,'export-'||i,'Текст','export-'||i,$2,i FROM generate_series(1,5001) i`,c.Id,f.ownerID)
 if err!=nil {t.Fatal(err)}
 api:=pb.NewUserExportServiceClient(f.conn)
 _,err=api.ExportUserContent(f.owner,&pb.ExportUserContentRequest{});requireCode(t,err,codes.ResourceExhausted)
 out,err:=api.ExportUserContent(f.owner,&pb.ExportUserContentRequest{CreatedBeforeUnixMs:3});if err!=nil||len(out.Entries)!=2 {t.Fatal(out,err)}
}
