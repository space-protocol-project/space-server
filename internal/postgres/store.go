package postgres

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"log"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Identity struct {
	ServerID  string
	PublicKey ed25519.PublicKey
}

type Store struct {
	pb.UnimplementedSyncServiceServer
	pb.UnimplementedAdminServiceServer
	pb.UnimplementedMembershipServiceServer
	pool        *pgxpool.Pool
	streamSlots chan struct{}
}

func Open(ctx context.Context, url string) (*Store, Identity, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, Identity{}, errors.New("Некорректная настройка SPACE_DATABASE_URL")
	}
	config.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, Identity{}, errors.New("Не удалось создать пул PostgreSQL")
	}
	store := &Store{pool: pool, streamSlots: make(chan struct{}, 64)}
	identity, err := store.initialize(ctx)
	if err != nil {
		pool.Close()
		return nil, Identity{}, err
	}
	return store, identity, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) initialize(ctx context.Context) (Identity, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Identity{}, errors.New("PostgreSQL недоступна")
	}
	defer tx.Rollback(ctx)
	// Все миграции и создание identity защищены одной транзакционной блокировкой.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(73616001)"); err != nil {
		return Identity{}, err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version integer PRIMARY KEY, checksum text NOT NULL)`); err != nil {
		return Identity{}, err
	}
	var initialized bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=1)").Scan(&initialized); err != nil {
		return Identity{}, err
	}
	if err := migrate(ctx, tx); err != nil {
		return Identity{}, err
	}
	identity, err := loadIdentity(ctx, tx, !initialized)
	if err != nil {
		return Identity{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func migrate(ctx context.Context, tx pgx.Tx) error {
	var future bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version > 5)").Scan(&future); err != nil {
		return err
	}
	if future {
		return errors.New("База создана более новой версией сервера")
	}
	for i, path := range []string{"001_initial.sql", "002_events_auth.sql", "003_space_settings.sql", "004_membership.sql", "005_recovery.sql"} {
		if err := applyMigration(ctx, tx, i+1, path); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, tx pgx.Tx, version int, path string) error {
	sql, err := migrations.ReadFile("migrations/" + path)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(strings.ReplaceAll(string(sql), "\r\n", "\n")))
	checksum := hex.EncodeToString(hash[:])
	var saved string
	err = tx.QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE version=$1", version).Scan(&saved)
	if err == nil {
		if saved != checksum {
			return errors.New("Контрольная сумма применённой миграции изменилась")
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version,checksum) VALUES ($1,$2)", version, checksum)
	return err
}

func loadIdentity(ctx context.Context, tx pgx.Tx, allowCreate bool) (Identity, error) {
	var identity Identity
	var seed []byte
	err := tx.QueryRow(ctx, "SELECT server_id, signing_seed FROM server_state WHERE singleton=true").Scan(&identity.ServerID, &seed)
	if errors.Is(err, pgx.ErrNoRows) {
		if !allowCreate {
			return Identity{}, errors.New("Идентичность существующего сервера отсутствует; требуется восстановление backup")
		}
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return Identity{}, err
		}
		randomID := make([]byte, 16)
		if _, err := rand.Read(randomID); err != nil {
			return Identity{}, err
		}
		identity = Identity{ServerID: "srv_" + hex.EncodeToString(randomID), PublicKey: public}
		_, err = tx.Exec(ctx, "INSERT INTO server_state(server_id,signing_seed) VALUES($1,$2)", identity.ServerID, private.Seed())
		return identity, err
	}
	if err != nil {
		return Identity{}, err
	}
	if len(seed) != ed25519.SeedSize {
		return Identity{}, errors.New("Повреждён серверный ключ")
	}
	identity.PublicKey = ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	return identity, nil
}

func databaseError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	// В ответ не попадают SQL, DSN и детали подключения.
	log.Printf("Ошибка операции PostgreSQL: %T", err)
	return status.Error(codes.Unavailable, "Хранилище временно недоступно")
}

func (s *Store) Create(ctx context.Context, req *pb.CreateContentRequest) (*pb.CreateContentResponse, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer tx.Rollback(ctx)
	if err := s.lockSession(ctx, tx); err != nil {
		return nil, err
	}
	if err := chatAccess(ctx, tx, true, true); err != nil {
		return nil, err
	}
	if err := enabledChat(ctx, tx, true); err != nil {
		return nil, err
	}
	var sequence int64
	// Блокировка канала задаёт порядок фиксации сообщений, без дыр от rollback.
	err = tx.QueryRow(ctx, "SELECT next_sequence FROM channels WHERE id=$1 FOR UPDATE", req.ChannelId).Scan(&sequence)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	previous := new(pb.Content)
	actor := authn.Actor(ctx)
	err = tx.QueryRow(ctx, "SELECT id, channel_id, text, author_id FROM contents WHERE channel_id=$1 AND author_id=$2 AND idempotency_key=$3", req.ChannelId, actor, req.IdempotencyKey).Scan(&previous.Id, &previous.ChannelId, &previous.Text, &previous.AuthorId)
	if err == nil {
		if previous.Text != req.Text {
			return nil, status.Error(codes.AlreadyExists, "Ключ уже использован с другим текстом")
		}
		return &pb.CreateContentResponse{Content: previous}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, databaseError(ctx, err)
	}
	if sequence > 1000 {
		return nil, status.Error(codes.ResourceExhausted, "Лимит прототипа: 1000 сообщений")
	}
	content := &pb.Content{Id: fmt.Sprintf("message-%d", sequence), ChannelId: req.ChannelId, Text: req.Text, AuthorId: actor}
	_, err = tx.Exec(ctx, "INSERT INTO contents(channel_id,sequence,id,text,idempotency_key,author_id) VALUES($1,$2,$3,$4,$5,$6)", req.ChannelId, sequence, content.Id, req.Text, req.IdempotencyKey, actor)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	if _, err = tx.Exec(ctx, "UPDATE channels SET next_sequence=next_sequence+1 WHERE id=$1", req.ChannelId); err != nil {
		return nil, databaseError(ctx, err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO events(channel_id,sequence,cursor,type) VALUES($1,$2,$3,'content.created')", req.ChannelId, sequence, fmt.Sprintf("event-%d", sequence)); err != nil {
		return nil, databaseError(ctx, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, databaseError(ctx, err)
	}
	return &pb.CreateContentResponse{Content: content}, nil
}

func (s *Store) List(ctx context.Context, req *pb.ListContentRequest) (*pb.ListContentResponse, error) {
	if err := chatAccess(ctx, s.pool, false, false); err != nil {
		return nil, err
	}
	if err := enabledChat(ctx, s.pool, false); err != nil {
		return nil, err
	}
	var after int64
	if req.After != "" {
		err := s.pool.QueryRow(ctx, "SELECT sequence FROM contents WHERE channel_id=$1 AND id=$2", req.ChannelId, req.After).Scan(&after)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Error(codes.InvalidArgument, "Неизвестный курсор")
		}
		if err != nil {
			return nil, databaseError(ctx, err)
		}
	}
	rows, err := s.pool.Query(ctx, "SELECT id,channel_id,text,author_id FROM contents WHERE channel_id=$1 AND sequence>$2 ORDER BY sequence LIMIT 100", req.ChannelId, after)
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	result := &pb.ListContentResponse{NextCursor: req.After}
	for rows.Next() {
		content := new(pb.Content)
		if err := rows.Scan(&content.Id, &content.ChannelId, &content.Text, &content.AuthorId); err != nil {
			return nil, databaseError(ctx, err)
		}
		result.Contents = append(result.Contents, content)
		result.NextCursor = content.Id
	}
	if err := rows.Err(); err != nil {
		return nil, databaseError(ctx, err)
	}
	return result, nil
}
