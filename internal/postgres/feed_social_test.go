package postgres

import (
 "testing"
 pb "github.com/space-protocol-project/space-server/gen/space/v1"
 "google.golang.org/grpc/codes"
)

func TestFeedSocialRepostsAndLiveRedaction(t *testing.T) {
 f := newChannelFixture(t)
 c := feedSection(t, f, "social")
 api := pb.NewFeedServiceClient(f.conn)
 original := feedCreate(t, f, f.owner, c.Id, "original")
 req := &pb.CreateFeedPostRequest{ChannelId:c.Id, RepostOf:original.Id, IdempotencyKey:"repost"}
 made, err := api.CreateFeedPost(f.owner, req)
 if err != nil { t.Fatal(err) }
 if made.Post.Text != "" || made.Post.RepostOf != original.Id || made.Post.GetQuotedPost().GetText() != original.Text { t.Fatal(made) }
 again, err := api.CreateFeedPost(f.owner, req)
 if err != nil || again.GetPost().GetId() != made.Post.Id { t.Fatal(again,err) }
 req.RepostOf = made.Post.Id
 _, err = api.CreateFeedPost(f.owner, req)
 requireCode(t,err,codes.AlreadyExists)
 req.IdempotencyKey = "nested"
 _, err = api.CreateFeedPost(f.owner,req)
 requireCode(t,err,codes.InvalidArgument)
 req.RepostOf,req.IdempotencyKey,req.Text = original.Id,"quote","Моё мнение"
 quote, err := api.CreateFeedPost(f.owner,req)
 if err != nil { t.Fatal(err) }
 if quote.Post.GetQuotedPost().GetId() != original.Id { t.Fatal(quote) }
 hidden,err := api.ModerateFeedPost(f.owner,&pb.ModerateFeedPostRequest{ChannelId:c.Id,PostId:original.Id,Hidden:true,Reason:"скрыть",ExpectedRevision:original.Revision})
 if err != nil { t.Fatal(err) }
 detail,err := api.GetFeedPost(f.owner,&pb.GetFeedPostRequest{ChannelId:c.Id,PostId:quote.Post.Id})
 if err != nil || detail.Post.QuotedPost != nil || detail.Post.Text != "Моё мнение" { t.Fatal(detail,err) }
 page,err := api.ListFeedPosts(f.owner,&pb.ListFeedPostsRequest{ChannelId:c.Id})
 if err != nil { t.Fatal(err) }
 for _,post := range page.Posts { if post.QuotedPost != nil { t.Fatal("Скрытый оригинал раскрылся",post) } }
 req.IdempotencyKey="hidden-source"
 _,err=api.CreateFeedPost(f.owner,req)
 requireCode(t,err,codes.NotFound)
 _,err=api.ModerateFeedPost(f.owner,&pb.ModerateFeedPostRequest{ChannelId:c.Id,PostId:original.Id,Reason:"вернуть",ExpectedRevision:hidden.Post.Revision})
 if err!=nil {t.Fatal(err)}
 detail,err=api.GetFeedPost(f.owner,&pb.GetFeedPostRequest{ChannelId:c.Id,PostId:quote.Post.Id})
 if err!=nil || detail.Post.GetQuotedPost().GetText()!=original.Text {t.Fatal(detail,err)}
 concealed,err:=api.ModerateFeedPost(f.owner,&pb.ModerateFeedPostRequest{ChannelId:c.Id,PostId:quote.Post.Id,Hidden:true,Reason:"скрыть цитату",ExpectedRevision:quote.Post.Revision})
 if err!=nil || concealed.Post.RepostOf!="" || concealed.Post.QuotedPost!=nil {t.Fatal(concealed,err)}
 other:=feedSection(t,f,"social_other")
 req.ChannelId,req.IdempotencyKey=other.Id,"cross-section"
 _,err=api.CreateFeedPost(f.owner,req)
 requireCode(t,err,codes.NotFound)
}

func TestFeedSocialReplyIdentityAndModeration(t *testing.T) {
 f:=newChannelFixture(t)
 c:=feedSection(t,f,"replies")
 api:=pb.NewFeedServiceClient(f.conn)
 root:=feedCreate(t,f,f.owner,c.Id,"root")
 first,err:=api.CommentFeedPost(f.owner,&pb.CommentFeedPostRequest{ChannelId:c.Id,PostId:root.Id,Text:"Первый",IdempotencyKey:"first"})
 if err!=nil {t.Fatal(err)}
 req:=&pb.CommentFeedPostRequest{ChannelId:c.Id,PostId:root.Id,Text:"Ответ",ReplyToCommentId:first.Comment.Id,IdempotencyKey:"reply"}
 second,err:=api.CommentFeedPost(f.owner,req)
 if err!=nil || second.Comment.ReplyToSequence!=first.Comment.Sequence || second.Comment.ReplyToCommentId!=first.Comment.Id {t.Fatal(second,err)}
 again,err:=api.CommentFeedPost(f.owner,req)
 if err!=nil || again.Comment.Id!=second.Comment.Id {t.Fatal(again,err)}
 req.ReplyToCommentId=""
 _,err=api.CommentFeedPost(f.owner,req)
 requireCode(t,err,codes.AlreadyExists)
 req.ReplyToCommentId=first.Comment.Id
 other:=feedCreate(t,f,f.owner,c.Id,"other")
 req.PostId,req.IdempotencyKey=other.Id,"wrong-parent"
 _,err=api.CommentFeedPost(f.owner,req)
 requireCode(t,err,codes.NotFound)
 _,err=api.ModerateFeedComment(f.owner,&pb.ModerateFeedCommentRequest{ChannelId:c.Id,PostId:root.Id,CommentId:first.Comment.Id,Hidden:true,Reason:"скрыть",ExpectedRevision:first.Comment.Revision})
 if err!=nil {t.Fatal(err)}
 req.PostId,req.IdempotencyKey=root.Id,"hidden-parent"
 _,err=api.CommentFeedPost(f.owner,req)
 requireCode(t,err,codes.NotFound)
 detail,err:=api.GetFeedPost(f.owner,&pb.GetFeedPostRequest{ChannelId:c.Id,PostId:root.Id})
 if err!=nil || len(detail.Comments)!=2 || detail.Comments[0].Text!="" || detail.Comments[1].Text!="Ответ" {t.Fatal(detail,err)}
 hidden,err:=api.ModerateFeedComment(f.owner,&pb.ModerateFeedCommentRequest{ChannelId:c.Id,PostId:root.Id,CommentId:second.Comment.Id,Hidden:true,Reason:"скрыть ответ",ExpectedRevision:second.Comment.Revision})
 if err!=nil || hidden.Comment.ReplyToCommentId!="" || hidden.Comment.ReplyToSequence!=0 {t.Fatal(hidden,err)}
}

func TestFeedSocialPublicationRights(t *testing.T) {
 f:=newChannelFixture(t)
 c:=feedSection(t,f,"social_acl")
 api:=pb.NewFeedServiceClient(f.conn)
 original:=feedCreate(t,f,f.owner,c.Id,"root")
 actor,_:=f.enroll(t,true)
 req:=&pb.CreateFeedPostRequest{ChannelId:c.Id,RepostOf:original.Id,IdempotencyKey:"repost"}
 _,err:=api.CreateFeedPost(actor,req)
 requireCode(t,err,codes.PermissionDenied)
 feedPolicy(t,f,c,feedRole("member",true,true))
 _,err=api.CreateFeedPost(actor,req)
 if err!=nil {t.Fatal(err)}
 feedPolicy(t,f,c,feedRole("member",false,true))
 _,err=api.CreateFeedPost(actor,req)
 requireCode(t,err,codes.PermissionDenied)
}
