package postgres

import (
    "encoding/base64"
    "errors"
    "strings"
    "testing"
    "github.com/jackc/pgx/v5/pgconn"
    pb "github.com/space-protocol-project/space-server/gen/space/v1"
    rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
    "github.com/space-protocol-project/space-server/internal/authn"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/metadata"
)

func TestSpaceSearchClosedTopicsManageWithoutReadAndMLS(t *testing.T) {
    f := newChannelFixture(t)
    sections := spaceSearchSeed(t,f)
    member, id := f.enroll(t,true)
    if _, err := f.store.pool.Exec(f.ctx,"UPDATE forum_threads SET closed=true"); err != nil { t.Fatal(err) }
    if page := spaceSearchPage(t,f,member,"совет"); len(page.Results) != 5 { t.Fatal("Закрытая тема читается",page) }
    f.acl(t,sections[1],principalRule(id,permissions(true,false,false,true)))
    if page := spaceSearchPage(t,f,member,"совет"); len(page.Results) != 3 { t.Fatal("Manage не заменяет read",page) }
    // Остатки обычного содержимого не должны раскрываться через поиск MLS-раздела.
    if _, err := f.store.pool.Exec(f.ctx,"UPDATE channels SET mls_group_id=decode(repeat('ab',16),'hex') WHERE id=$1",sections[0].Id); err != nil { t.Fatal(err) }
    if page := spaceSearchPage(t,f,member,"совет"); len(page.Results) != 2 { t.Fatal("MLS исключён",page) }
    if _, err := f.contents.CreateContent(f.owner,&pb.CreateContentRequest{ChannelId:"general",Text:strings.Repeat("片",300),IdempotencyKey:"snippet"}); err != nil { t.Fatal(err) }
    page := spaceSearchPage(t,f,member,"片")
    if len(page.Results) != 1 || len([]rune(page.Results[0].Snippet)) != 240 { t.Fatal(page) }
}

func TestSpaceSearchRejectsMalformedCursorFields(t *testing.T) {
    f := newChannelFixture(t)
    member, id := f.enroll(t,true)
    binding := searchBinding("space-search-v1","совет",id)
    valid := `{"v":1,"b":"`+binding+`","t":1,"k":2,"c":"general","i":"target"}`
    api := pb.NewSpaceSearchServiceClient(f.conn)
    for _, source := range []string{
        strings.Replace(valid,`"k":2`,`"k":6`,1),
        strings.Replace(valid,`"t":1`,`"t":-1`,1),
        strings.Replace(valid,`"c":"general"`,`"c":"bad space"`,1),
        strings.TrimSuffix(valid,"}")+`,"extra":true}`,
        valid+` {}`,
    } {
        token := base64.RawURLEncoding.EncodeToString([]byte(source))
        _, err := api.SearchSpace(member,&pb.SearchSpaceRequest{Query:"совет",PageToken:token})
        requireCode(t,err,codes.InvalidArgument)
    }
}

func TestSpaceSearchHoldsChannelReadLocks(t *testing.T) {
    f := newChannelFixture(t)
    channel := f.create(t,"lock_search")
    md, _ := metadata.FromOutgoingContext(f.owner)
    ctx, err := authn.AuthorizeCall(metadata.NewIncomingContext(f.ctx,md),f.store,rpc.SpaceSearchServiceSearchSpaceProcedure)
    if err != nil { t.Fatal(err) }
    tx, _, err := f.store.spaceSearchTransaction(ctx)
    if err != nil { t.Fatal(err) }
    defer tx.Rollback(f.ctx)
    writer, err := f.store.pool.Begin(f.ctx)
    if err != nil { t.Fatal(err) }
    defer writer.Rollback(f.ctx)
    if _, err = writer.Exec(f.ctx,"SET LOCAL lock_timeout='100ms'"); err != nil { t.Fatal(err) }
    _, err = writer.Exec(f.ctx,"UPDATE channels SET revision=revision+1 WHERE id=$1",channel.Id)
    var blocked *pgconn.PgError
    if !errors.As(err,&blocked) || blocked.Code != "55P03" { t.Fatalf("Изменение ACL не блокируется: %v",err) }
    writer.Rollback(f.ctx)
    tx.Rollback(f.ctx)
    if _, err = f.store.pool.Exec(f.ctx,"UPDATE channels SET revision=revision+1 WHERE id=$1",channel.Id); err != nil { t.Fatal(err) }
}
