package postgres

import (
 "crypto/ed25519"
 "crypto/rand"
 "net"
 "slices"
 "testing"

 pb "github.com/space-protocol-project/space-server/gen/space/v1"
 "github.com/space-protocol-project/space-server/internal/authn"
 "google.golang.org/grpc"
 "google.golang.org/grpc/codes"
 "google.golang.org/grpc/credentials/insecure"
 "google.golang.org/grpc/metadata"
 "google.golang.org/grpc/status"
)

func TestPairingFullAndLimitedAuthority(t *testing.T) {
 ctx, url := isolatedDatabase(t)
 store, identity, err := openManual(ctx, url)
 if err != nil { t.Fatal(err) }; defer store.Close()
 listener, err := net.Listen("tcp", "127.0.0.1:0")
 if err != nil { t.Fatal(err) }
 server := grpc.NewServer(grpc.UnaryInterceptor(authn.Interceptor(store)))
 pb.RegisterAuthServiceServer(server, NewAuth(store, identity.ServerID, "http://127.0.0.1:8080"))
 go server.Serve(listener); defer server.Stop()
 conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
 if err != nil { t.Fatal(err) }; defer conn.Close()
 auth := pb.NewAuthServiceClient(conn)
 root, rootPrivate, _ := ed25519.GenerateKey(rand.Reader)
 source, sourcePrivate, _ := ed25519.GenerateKey(rand.Reader)
 sourceGrant := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose:"device.register", RootPublicKey:root, DevicePublicKey:source}, rootPrivate)
 sourceLogin := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose:"auth.login", GrantId:sourceGrant.GrantId}, sourcePrivate)
 sourceContext := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer " + sourceLogin.AccessToken)
 target, targetPrivate, _ := ed25519.GenerateKey(rand.Reader)
 if _, err := auth.CreatePairing(ctx, &pb.CreatePairingRequest{PublicKey:target, DeviceName:"full", FullManagement:true}); status.Code(err) != codes.InvalidArgument { t.Fatal("Несогласованный режим",err) }
 pair, err := auth.CreatePairing(ctx, &pb.CreatePairingRequest{PublicKey:target, DeviceName:"full", Administrative:true, FullManagement:true})
 if err != nil { t.Fatal(err) }
 if _, err := auth.ProposePairing(sourceContext, &pb.ProposePairingRequest{PairingId:pair.Pairing.Id}); err != nil { t.Fatal(err) }
 req := &pb.CreateChallengeRequest{Purpose:"device.register", RootPublicKey:root, DevicePublicKey:target, Administrative:true, PairingId:pair.Pairing.Id}
 if _, err := auth.CreateChallenge(ctx, req); status.Code(err) != codes.FailedPrecondition { t.Fatal("Подмена режима",err) }
 req.FullManagement = true
 full := complete(t, ctx, auth, req, rootPrivate)
 child, childPrivate, _ := ed25519.GenerateKey(rand.Reader)
 delegation := &pb.CreateChallengeRequest{Purpose:"device.delegate", RecoveryGrantId:full.GrantId, DevicePublicKey:child}
 if _, err := auth.CreateChallenge(ctx, delegation); status.Code(err) != codes.Unauthenticated { t.Fatal("Делегирование до claim",err) }
 fullLogin := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose:"auth.login", GrantId:full.GrantId}, targetPrivate)
 fullContext := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer " + fullLogin.AccessToken)
 if _, err := auth.ClaimPairing(fullContext, &pb.ClaimPairingRequest{PairingId:pair.Pairing.Id}); err != nil { t.Fatal(err) }
 var scopes []string
 if err := store.pool.QueryRow(ctx,"SELECT scopes FROM device_grants WHERE id=$1",full.GrantId).Scan(&scopes); err != nil { t.Fatal(err) }
 if !slices.Equal(scopes, []string{"chat.read","chat.write","space.manage","device.manage"}) { t.Fatal(scopes) }
 var role string
 if err := store.pool.QueryRow(ctx,"SELECT role FROM memberships WHERE principal_id=$1",full.PrincipalId).Scan(&role); err != nil || role != "member" { t.Fatal("Повышение роли",role,err) }
 delegation.FullManagement = true
 if _, err := auth.CreateChallenge(ctx, delegation); status.Code(err) != codes.InvalidArgument { t.Fatal("Повторное делегирование полного режима",err) }
 delegation.FullManagement = false
 limited := complete(t, ctx, auth, delegation, targetPrivate)
 limitedLogin := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose:"auth.login", GrantId:limited.GrantId}, childPrivate)
 if _, err := auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose:"device.delegate", RecoveryGrantId:limited.GrantId, DevicePublicKey:source}); status.Code(err) != codes.Unauthenticated { t.Fatal("Ограниченный стал управляющим",err) }
 if _, err := auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose:"recovery.device.revoke", RecoveryGrantId:limited.GrantId, GrantId:full.GrantId}); status.Code(err) != codes.Unauthenticated { t.Fatal("Ограниченный отзывает управляющего",err) }
 newRoot, _, _ := ed25519.GenerateKey(rand.Reader)
 newDevice, _, _ := ed25519.GenerateKey(rand.Reader)
 if _, err := auth.CreateRootRotation(fullContext, &pb.CreateRootRotationRequest{Profile:"root-rotation-v1", NewRootPublicKey:newRoot, NewDevicePublicKey:newDevice, OperationId:"pairing-authority-test"}); status.Code(err) != codes.Unauthenticated { t.Fatal("Рабочий управляющий меняет root",err) }
 if err := store.pool.QueryRow(ctx,"SELECT scopes FROM device_grants WHERE id=$1",limited.GrantId).Scan(&scopes); err != nil || !slices.Equal(scopes, []string{"chat.read","chat.write"}) { t.Fatal(scopes,err) }
 // Подпись управляющего отзывает только устройство той же личности.
 foreignRoot, foreignPrivate, _ := ed25519.GenerateKey(rand.Reader)
 foreign := complete(t, ctx, auth, &pb.CreateChallengeRequest{Purpose:"device.register", RootPublicKey:foreignRoot, DevicePublicKey:source}, foreignPrivate)
 if _, err := auth.CreateChallenge(ctx, &pb.CreateChallengeRequest{Purpose:"recovery.device.revoke", RecoveryGrantId:full.GrantId, GrantId:foreign.GrantId}); status.Code(err) != codes.Unauthenticated { t.Fatal("Чужая личность",err) }
 complete(t,ctx,auth,&pb.CreateChallengeRequest{Purpose:"recovery.device.revoke", RecoveryGrantId:full.GrantId, GrantId:sourceGrant.GrantId},targetPrivate)
 if _, err := auth.RevokeCurrentDevice(fullContext,&pb.RevokeCurrentDeviceRequest{}); err != nil { t.Fatal(err) }
 limitedContext := metadata.AppendToOutgoingContext(ctx,"authorization","Bearer "+limitedLogin.AccessToken)
 if _, err := auth.ListDevices(limitedContext,&pb.ListDevicesRequest{}); status.Code(err) != codes.Unauthenticated { t.Fatal("Дочерняя сессия пережила отзыв",err) }
}
