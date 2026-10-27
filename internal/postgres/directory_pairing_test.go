package postgres

import (
	"bytes"
	"crypto/sha256"
	"net"
	"sync"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func directoryTestClient(t *testing.T, store *Store) pb.DirectoryPairingServiceClient {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(authn.Interceptor(store)))
	pb.RegisterDirectoryPairingServiceServer(server, store)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return pb.NewDirectoryPairingServiceClient(conn)
}

func TestDirectoryPairingCapabilitiesAndOrdering(t *testing.T) {
	ctx, url := isolatedDatabase(t)
	store, _, err := openManual(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	api := directoryTestClient(t, store)
	if _, err := api.CreateDirectoryTransfer(ctx, &pb.CreateDirectoryTransferRequest{Profile: "custom"}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	lease, err := api.CreateDirectoryTransfer(ctx, &pb.CreateDirectoryTransferRequest{Profile: directoryNoiseProfile})
	if err != nil {
		t.Fatal(err)
	}
	if lease.ExpiresAt-lease.CreatedAt != 300 {
		t.Fatal(lease)
	}
	var hash, code []byte
	if err := store.pool.QueryRow(ctx, "SELECT owner_hash,code_hash FROM directory_transfers WHERE id=$1", lease.Id).Scan(&hash, &code); err != nil {
		t.Fatal(err)
	}
	ownerHash, codeHash := sha256.Sum256([]byte(lease.OwnerToken)), sha256.Sum256([]byte(lease.Code))
	if !bytes.Equal(hash, ownerHash[:]) || !bytes.Equal(code, codeHash[:]) {
		t.Fatal("Открытые capability в БД")
	}
	peer, err := api.OpenDirectoryTransfer(ctx, &pb.OpenDirectoryTransferRequest{Code: lease.Code})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.OpenDirectoryTransfer(ctx, &pb.OpenDirectoryTransferRequest{Code: lease.Code}); status.Code(err) != codes.NotFound {
		t.Fatal("Повтор code", err)
	}
	other, err := api.CreateDirectoryTransfer(ctx, &pb.CreateDirectoryTransferRequest{Profile: directoryNoiseProfile})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.ReadDirectoryFrame(ctx, &pb.ReadDirectoryFrameRequest{Id: lease.Id, Token: other.OwnerToken}); status.Code(err) != codes.NotFound {
		t.Fatal("Чужая capability", err)
	}
	write := func(seq uint32, token string, data []byte, final bool) error {
		_, err := api.WriteDirectoryFrame(ctx, &pb.WriteDirectoryFrameRequest{Id: lease.Id, Token: token, Sequence: seq, Data: data, FinalRecord: final})
		return err
	}
	if err := write(1, peer.PeerToken, []byte{1}, false); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("Пропуск slot", err)
	}
	if err := write(0, peer.PeerToken, []byte{1}, false); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("Подмена роли", err)
	}
	for seq := uint32(0); seq < 4; seq++ {
		token := lease.OwnerToken
		if seq == 1 {
			token = peer.PeerToken
		}
		if err := write(seq, token, []byte{byte(seq + 1)}, false); err != nil {
			t.Fatal(err)
		}
		if err := write(seq, token, []byte{byte(seq + 1)}, false); err != nil {
			t.Fatal("Идемпотентный повтор", err)
		}
		if err := write(seq, token, []byte{99}, false); status.Code(err) != codes.Aborted {
			t.Fatal("Перезапись", err)
		}
	}
	frame, err := api.ReadDirectoryFrame(ctx, &pb.ReadDirectoryFrameRequest{Id: lease.Id, Token: peer.PeerToken, Sequence: 3})
	if err != nil || !frame.Available || !bytes.Equal(frame.Data, []byte{4}) {
		t.Fatal(frame, err)
	}
	if err := write(4, peer.PeerToken, make([]byte, 65536), false); status.Code(err) != codes.ResourceExhausted {
		t.Fatal(err)
	}
	if err := write(4, peer.PeerToken, []byte{5}, true); err != nil {
		t.Fatal(err)
	}
	if err := write(5, peer.PeerToken, []byte{6}, false); status.Code(err) != codes.FailedPrecondition {
		t.Fatal("После final", err)
	}
	if err := write(5, lease.OwnerToken, make([]byte, 257), false); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	if err := write(5, lease.OwnerToken, []byte{6}, false); err != nil {
		t.Fatal(err)
	}
	if err := write(6, lease.OwnerToken, []byte{7}, false); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	if _, err := api.CloseDirectoryTransfer(ctx, &pb.CloseDirectoryTransferRequest{Id: lease.Id, Token: peer.PeerToken}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM directory_transfer_frames WHERE transfer_id=$1", lease.Id).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestDirectoryPairingRestartExpiryAndConcurrentClaim(t *testing.T) {
	ctx, url := isolatedDatabase(t)
	store, _, err := openManual(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.CreateDirectoryTransfer(ctx, &pb.CreateDirectoryTransferRequest{Profile: directoryNoiseProfile})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.OpenDirectoryTransfer(ctx, &pb.OpenDirectoryTransferRequest{Code: lease.Code})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if status.Code(err) != codes.NotFound {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatal(successes)
	}
	if _, err := store.WriteDirectoryFrame(ctx, &pb.WriteDirectoryFrameRequest{Id: lease.Id, Token: lease.OwnerToken, Data: []byte{1, 2, 3}}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, _, err = openManual(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	frame, err := store.ReadDirectoryFrame(ctx, &pb.ReadDirectoryFrameRequest{Id: lease.Id, Token: lease.OwnerToken})
	if err != nil || !bytes.Equal(frame.Data, []byte{1, 2, 3}) {
		t.Fatal(frame, err)
	}
	if _, err := store.pool.Exec(ctx, "UPDATE directory_transfers SET expires_at=now()-interval '1 second' WHERE id=$1", lease.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadDirectoryFrame(ctx, &pb.ReadDirectoryFrameRequest{Id: lease.Id, Token: lease.OwnerToken}); status.Code(err) != codes.NotFound {
		t.Fatal("Истёкшая capability", err)
	}
	if _, err := store.CreateDirectoryTransfer(ctx, &pb.CreateDirectoryTransferRequest{Profile: directoryNoiseProfile}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM directory_transfer_frames WHERE transfer_id=$1", lease.Id).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestDirectoryPairingQuotasAndNoIdentityChanges(t *testing.T) {
	ctx, url := isolatedDatabase(t)
	store, _, err := openManual(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	count := func(table string) int {
		var n int
		if err := store.pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	grants, members := count("device_grants"), count("memberships")
	var leases []*pb.CreateDirectoryTransferResponse
	for i := 0; i < 32; i++ {
		lease, err := store.CreateDirectoryTransfer(ctx, &pb.CreateDirectoryTransferRequest{Profile: directoryNoiseProfile})
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, lease)
	}
	if _, err := store.CreateDirectoryTransfer(ctx, &pb.CreateDirectoryTransferRequest{Profile: directoryNoiseProfile}); status.Code(err) != codes.ResourceExhausted {
		t.Fatal(err)
	}
	lease := leases[0]
	peer, err := store.OpenDirectoryTransfer(ctx, &pb.OpenDirectoryTransferRequest{Code: lease.Code})
	if err != nil {
		t.Fatal(err)
	}
	for seq := uint32(0); seq < 80; seq++ {
		token := peer.PeerToken
		if seq == 0 || seq == 2 || seq == 3 {
			token = lease.OwnerToken
		}
		_, err := store.WriteDirectoryFrame(ctx, &pb.WriteDirectoryFrameRequest{Id: lease.Id, Token: token, Sequence: seq, Data: make([]byte, 65535)})
		if err != nil {
			t.Fatal(seq, err)
		}
	}
	if _, err := store.ReadDirectoryFrame(ctx, &pb.ReadDirectoryFrameRequest{Id: lease.Id, Token: lease.OwnerToken, Sequence: 80}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	if grants != count("device_grants") || members != count("memberships") {
		t.Fatal("Relay изменил доступ")
	}
}
