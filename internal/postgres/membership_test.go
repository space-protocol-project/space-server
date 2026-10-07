package postgres

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"sync"
	"testing"

	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"github.com/space-protocol-project/space-protocol/server/internal/chat"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestInvitationsAndLiveMembership(t *testing.T) {
	ctx, url := isolatedDatabase(t)
	store, identity, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(authn.Interceptor(store)), grpc.StreamInterceptor(authn.StreamInterceptor(store)))
	pb.RegisterAuthServiceServer(server, NewAuth(store, identity.ServerID, "http://127.0.0.1:8080"))
	pb.RegisterAdminServiceServer(server, store)
	pb.RegisterMembershipServiceServer(server, store)
	pb.RegisterSyncServiceServer(server, store)
	pb.RegisterContentServiceServer(server, chat.NewPersistent(identity.ServerID, store))
	go server.Serve(listener)
	defer server.Stop()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	auth := pb.NewAuthServiceClient(conn)
	admin := pb.NewAdminServiceClient(conn)
	members := pb.NewMembershipServiceClient(conn)
	content := pb.NewContentServiceClient(conn)
	enroll := func(invite string) (context.Context, string, error) {
		root, private, _ := ed25519.GenerateKey(rand.Reader)
		device, devicePrivate, _ := ed25519.GenerateKey(rand.Reader)
		challenge, err := auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose: "device.register", RootPublicKey: root, DevicePublicKey: device, Administrative: true})
		if err != nil {
			return nil, "", err
		}
		proof := signedProof(t, challenge, private)
		proof.InvitationToken = invite
		grant, err := auth.CompleteChallenge(ctx, proof)
		if err != nil {
			return nil, "", err
		}
		session := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose: "auth.login", GrantId: grant.GrantId}, devicePrivate)
		return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+session.AccessToken), grant.PrincipalId, nil
	}
	owner, ownerID, err := enroll("")
	if err != nil {
		t.Fatal(err)
	}
	code, err := store.CreateSetupCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := admin.ClaimOwner(owner, &pb.ClaimOwnerRequest{SetupCode: code})
	if err != nil {
		t.Fatal(err)
	}
	update := &pb.UpdateSettingsRequest{Title: "Закрытое пространство", ChatTitle: "Чат", ChatEnabled: true, RegistrationPolicy: "closed", ExpectedRevision: claimed.Settings.Revision}
	if _, err = admin.UpdateSettings(owner, update); err != nil {
		t.Fatal(err)
	}
	if _, _, err = enroll(""); status.Code(err) != codes.PermissionDenied {
		t.Fatal("Регистрация без приглашения", err)
	}
	if _, err = admin.CreateInvite(owner, &pb.CreateInviteRequest{Role: "admin", TtlSeconds: 3600, MaxUses: 1}); status.Code(err) != codes.InvalidArgument {
		t.Fatal("Приглашение администратора", err)
	}
	invite, err := admin.CreateInvite(owner, &pb.CreateInviteRequest{Role: "reader", TtlSeconds: 3600, MaxUses: 1})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := members.PreviewInvite(ctx, &pb.PreviewInviteRequest{Token: invite.Token})
	if err != nil || preview.Role != "reader" || preview.ServerId != identity.ServerID {
		t.Fatal(preview, err)
	}
	var winner context.Context
	var winnerID string
	var mu sync.Mutex
	var wg sync.WaitGroup
	wins := 0
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			actor, id, err := enroll(invite.Token)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
				winner = actor
				winnerID = id
			} else if status.Code(err) != codes.PermissionDenied {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("Одноразовое приглашение использовали %d пользователей", wins)
	}
	// Повтор того же участника идемпотентен даже после исчерпания количества мест.
	if _, err = members.AcceptInvite(winner, &pb.AcceptInviteRequest{Token: invite.Token}); err != nil {
		t.Fatal(err)
	}
	var uses int
	if err = store.pool.QueryRow(ctx, "SELECT uses FROM invitations WHERE id=$1", invite.Invite.Id).Scan(&uses); err != nil || uses != 1 {
		t.Fatal(uses, err)
	}
	if _, err = content.ListContent(winner, &pb.ListContentRequest{ChannelId: "general"}); err != nil {
		t.Fatal(err)
	}
	if _, err = content.CreateContent(winner, &pb.CreateContentRequest{ChannelId: "general", Text: "Запрещено", IdempotencyKey: "reader"}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("Читатель написал", err)
	}
	if _, err = admin.CreateInvite(winner, &pb.CreateInviteRequest{Role: "member", TtlSeconds: 3600, MaxUses: 1}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("Читатель пригласил", err)
	}
	current, err := members.GetMembership(winner, &pb.GetMembershipRequest{})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := admin.UpdateMember(owner, &pb.UpdateMemberRequest{PrincipalId: winnerID, Role: "admin", ExpectedRevision: current.Member.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.GetSettings(winner, &pb.GetSettingsRequest{}); err != nil {
		t.Fatal("Администратор не получил настройки", err)
	}
	if _, err = admin.UpdateMember(winner, &pb.UpdateMemberRequest{PrincipalId: ownerID, Role: "reader", ExpectedRevision: 1}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("Администратор изменил владельца", err)
	}
	if _, err = admin.UpdateMember(owner, &pb.UpdateMemberRequest{PrincipalId: ownerID, Role: "reader", ExpectedRevision: 1}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("Владелец потерял роль", err)
	}
	stream, err := pb.NewSyncServiceClient(conn).Subscribe(winner, &pb.SubscribeRequest{ChannelId: "general"})
	if err != nil {
		t.Fatal(err)
	}
	if frame, err := stream.Recv(); err != nil || !frame.Heartbeat {
		t.Fatal(frame, err)
	}
	block := &pb.UpdateMemberRequest{PrincipalId: winnerID, Role: "reader", Blocked: true, ExpectedRevision: changed.Member.Revision}
	if _, err = admin.UpdateMember(owner, block); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.UpdateMember(owner, block); status.Code(err) != codes.Aborted {
		t.Fatal("Конфликт прав", err)
	}
	if _, err = stream.Recv(); status.Code(err) != codes.PermissionDenied {
		t.Fatal("Подписка пережила блокировку", err)
	}
	if _, err = content.ListContent(winner, &pb.ListContentRequest{ChannelId: "general"}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("Чтение после блокировки", err)
	}
	if _, err = pb.NewSyncServiceClient(conn).ListEvents(winner, &pb.ListEventsRequest{ChannelId: "general"}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("События после блокировки", err)
	}
	if _, err = admin.GetSettings(winner, &pb.GetSettingsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("Управление после блокировки", err)
	}
	if _, err = members.AcceptInvite(winner, &pb.AcceptInviteRequest{Token: invite.Token}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("Приглашение сняло блокировку", err)
	}
	if _, err = admin.RevokeInvite(owner, &pb.RevokeInviteRequest{InviteId: invite.Invite.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err = members.PreviewInvite(ctx, &pb.PreviewInviteRequest{Token: invite.Token}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("Отозванное приглашение", err)
	}
	expired, err := admin.CreateInvite(owner, &pb.CreateInviteRequest{Role: "member", TtlSeconds: 60, MaxUses: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.pool.Exec(ctx, "UPDATE invitations SET expires_at=now()-interval '1 second' WHERE id=$1", expired.Invite.Id); err != nil {
		t.Fatal(err)
	}
	if _, _, err = enroll(expired.Token); status.Code(err) != codes.PermissionDenied {
		t.Fatal("Истёкшее приглашение", err)
	}
}
