package postgres

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/chat"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func isolatedDatabase(t *testing.T) (context.Context, string) {
	t.Helper()
	connectionURL := os.Getenv("SPACE_TEST_DATABASE_URL")
	if connectionURL == "" {
		t.Skip("SPACE_TEST_DATABASE_URL не задана: проверка PostgreSQL выполняется в CI")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	admin, err := pgxpool.New(ctx, connectionURL)
	if err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schema := "space_test_" + hex.EncodeToString(random)
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	parsed, err := url.Parse(connectionURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return ctx, parsed.String()
}

func TestConcurrentInitializationAndRestart(t *testing.T) {
	ctx, databaseURL := isolatedDatabase(t)
	const count = 6
	stores := make([]*Store, count)
	identities := make([]Identity, count)
	var workers sync.WaitGroup
	for i := 0; i < count; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			var err error
			stores[i], identities[i], err = Open(ctx, databaseURL)
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	workers.Wait()
	for i, store := range stores {
		if store == nil {
			t.Fatal("Инициализация не завершена")
		}
		t.Cleanup(store.Close)
		if identities[i].ServerID != identities[0].ServerID || !bytes.Equal(identities[i].PublicKey, identities[0].PublicKey) {
			t.Fatal("Конкурентные запуски создали разные identities")
		}
	}
	service := chat.NewPersistent(identities[0].ServerID, stores[0])
	request := &pb.CreateContentRequest{ChannelId: "general", Text: "Сохраняем после перезапуска", IdempotencyKey: "same-request"}
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			s := chat.NewPersistent(identities[i%count].ServerID, stores[i%count])
			if _, err := s.CreateContent(ctx, request); err != nil {
				t.Error(err)
			}
		}(i)
	}
	workers.Wait()
	listed, err := service.ListContent(ctx, &pb.ListContentRequest{ChannelId: "general"})
	if err != nil || len(listed.GetContents()) != 1 {
		t.Fatalf("%v %v", listed, err)
	}
	originalID := listed.Contents[0].Id
	for _, store := range stores {
		store.Close()
	}
	restarted, identity, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if identity.ServerID != identities[0].ServerID || !bytes.Equal(identity.PublicKey, identities[0].PublicKey) {
		t.Fatal("Identity изменилась после restart")
	}
	service = chat.NewPersistent(identity.ServerID, restarted)
	repeated, err := service.CreateContent(ctx, request)
	if err != nil || repeated.GetContent().GetId() != originalID {
		t.Fatalf("Повтор после restart: %v %v", repeated, err)
	}
	var seed []byte
	if err := restarted.pool.QueryRow(ctx, "SELECT signing_seed FROM server_state").Scan(&seed); err != nil {
		t.Fatal(err)
	}
	message := []byte("тест серверного ключа")
	if !ed25519.Verify(identities[0].PublicKey, message, ed25519.Sign(ed25519.NewKeyFromSeed(seed), message)) {
		t.Fatal("Ключ не сохранён")
	}
	conflict := &pb.CreateContentRequest{ChannelId: "general", Text: "Изменённый текст", IdempotencyKey: request.IdempotencyKey}
	if _, err := service.CreateContent(ctx, conflict); status.Code(err) != codes.AlreadyExists {
		t.Fatal(err)
	}
	next, err := service.CreateContent(ctx, &pb.CreateContentRequest{ChannelId: "general", Text: "Следующее сообщение", IdempotencyKey: "next"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.ListContent(ctx, &pb.ListContentRequest{ChannelId: "general", After: originalID})
	if err != nil || len(page.GetContents()) != 1 || page.NextCursor != next.Content.Id {
		t.Fatalf("Курсор: %v %v", page, err)
	}
	if _, err := service.ListContent(ctx, &pb.ListContentRequest{ChannelId: "general", After: "invalid"}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			_, err := service.CreateContent(ctx, &pb.CreateContentRequest{ChannelId: "general", Text: "Параллельное сообщение", IdempotencyKey: fmt.Sprintf("distinct-%d", i)})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	workers.Wait()
	first, err := service.ListContent(ctx, &pb.ListContentRequest{ChannelId: "general"})
	if err != nil || len(first.GetContents()) != 100 {
		t.Fatalf("Первая страница: %v %v", first, err)
	}
	second, err := service.ListContent(ctx, &pb.ListContentRequest{ChannelId: "general", After: first.NextCursor})
	if err != nil || len(second.GetContents()) != 7 {
		t.Fatalf("Вторая страница: %v %v", second, err)
	}
}

func TestDamagedMigrationAndMissingIdentityFailClosed(t *testing.T) {
	ctx, databaseURL := isolatedDatabase(t)
	store, _, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var checksum string
	if err := store.pool.QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE version=1").Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "UPDATE schema_migrations SET checksum='damaged' WHERE version=1"); err != nil {
		t.Fatal(err)
	}
	if reopened, _, err := Open(ctx, databaseURL); err == nil {
		reopened.Close()
		t.Fatal("Повреждённая миграция принята")
	}
	if _, err := store.pool.Exec(ctx, "UPDATE schema_migrations SET checksum=$1 WHERE version=1", checksum); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "INSERT INTO schema_migrations VALUES(9,'future')"); err != nil {
		t.Fatal(err)
	}
	if reopened, _, err := Open(ctx, databaseURL); err == nil {
		reopened.Close()
		t.Fatal("Новая схема принята старым сервером")
	}
	if _, err := store.pool.Exec(ctx, "DELETE FROM schema_migrations WHERE version=9"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "DELETE FROM server_state"); err != nil {
		t.Fatal(err)
	}
	if reopened, _, err := Open(ctx, databaseURL); err == nil {
		reopened.Close()
		t.Fatal("Утраченная identity заменена новой")
	}
}
