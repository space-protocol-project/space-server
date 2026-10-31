package postgres

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
)

//go:embed extensions_p1_outbox_schema.sql
var extensionP1OutboxSchema string

// ExtensionP1Outbox доступен только доверенному коду ядра, не RPC и не WASM.
// Обработчик обязан дедуплицировать эффект по ID и проверять права получателей.
type ExtensionP1Outbox struct {
	store                            *Store
	installation, instance, consumer string
}

type ExtensionP1Delivery struct {
	Installation, Instance, ID, Type string
	Payload                          []byte
	Revision                         int64
	Ordinal                          int32
	Token                            string
	Until                            time.Time
	Attempt                          int64
}

// NewExtensionP1Outbox закрепляет одного обработчика; смена и fan-out пока запрещены.
// Не вызывается рабочим сервером и не удаляет доставленные события.
func (s *Store) NewExtensionP1Outbox(ctx context.Context, installation, instance, consumer string) (*ExtensionP1Outbox, error) {
	if !channelIDPattern.MatchString(installation) || !channelIDPattern.MatchString(instance) || !channelIDPattern.MatchString(consumer) {
		return nil, mutationError(codes.InvalidArgument, "invalid_consumer_binding")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, mutationDatabaseError(err)
	}
	defer tx.Rollback(ctx)
	var version int
	if err = tx.QueryRow(ctx, "SELECT version FROM extension_p1_meta WHERE singleton=true").Scan(&version); err != nil {
		return nil, mutationDatabaseError(err)
	}
	if version != 2 {
		return nil, mutationError(codes.FailedPrecondition, "storage_version")
	}
	if _, err = tx.Exec(ctx, extensionP1OutboxSchema); err != nil {
		return nil, mutationDatabaseError(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO extension_p1_delivery(installation,instance,consumer) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, installation, instance, consumer)
	if err != nil {
		return nil, mutationDatabaseError(err)
	}
	var registered string
	if err = tx.QueryRow(ctx, `SELECT consumer FROM extension_p1_delivery WHERE installation=$1 AND instance=$2`, installation, instance).Scan(&registered); err != nil {
		return nil, mutationDatabaseError(err)
	}
	if registered != consumer {
		return nil, mutationError(codes.FailedPrecondition, "consumer_binding_changed")
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, mutationDatabaseError(err)
	}
	return &ExtensionP1Outbox{s, installation, instance, consumer}, nil
}

// Claim выдаёт самое раннее событие; активная аренда закрывает выдачу следующих.
// Время аренды (30 секунд) вычисляет БД. Payload никогда не исполняется внутри SQL.
func (o *ExtensionP1Outbox) Claim(ctx context.Context) (*ExtensionP1Delivery, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := o.store.pool.Begin(ctx)
	if err != nil {
		return nil, mutationDatabaseError(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='1s'; SET LOCAL statement_timeout='1500ms'`); err != nil {
		return nil, mutationDatabaseError(err)
	}
	var revision int64
	var ordinal int32
	var busy bool
	err = tx.QueryRow(ctx, `SELECT ack_revision,ack_ordinal,COALESCE(lease_until>clock_timestamp(),false)
 FROM extension_p1_delivery WHERE installation=$1 AND instance=$2 AND consumer=$3 FOR UPDATE`, o.installation, o.instance, o.consumer).Scan(&revision, &ordinal, &busy)
	if err != nil {
		return nil, mutationDatabaseError(err)
	}
	if busy {
		return nil, nil
	}
	event := &ExtensionP1Delivery{Installation: o.installation, Instance: o.instance}
	err = tx.QueryRow(ctx, `SELECT event_id,revision,ordinal,type,payload FROM extension_p1_outbox
 WHERE installation=$1 AND instance=$2 AND (revision,ordinal)>($3,$4) ORDER BY revision,ordinal LIMIT 1`, o.installation, o.instance, revision, ordinal).Scan(&event.ID, &event.Revision, &event.Ordinal, &event.Type, &event.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mutationDatabaseError(err)
	}
	var token [32]byte
	if _, err = rand.Read(token[:]); err != nil {
		return nil, err
	}
	event.Token = hex.EncodeToString(token[:])
	err = tx.QueryRow(ctx, `UPDATE extension_p1_delivery SET lease_revision=$3,lease_ordinal=$4,lease_token=$5,
 lease_until=clock_timestamp()+interval '30 seconds',attempts=attempts+1 WHERE installation=$1 AND instance=$2 RETURNING lease_until,attempts`, o.installation, o.instance, event.Revision, event.Ordinal, event.Token).Scan(&event.Until, &event.Attempt)
	if err != nil {
		return nil, mutationDatabaseError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, mutationDatabaseError(err)
	}
	return event, nil
}

// Ack принимает только текущую неистёкшую аренду. Повтор последнего подтверждения
// безопасен; старый token после новой выдачи не может подтвердить новое событие.
func (o *ExtensionP1Outbox) Ack(ctx context.Context, token string) error {
	if len(token) != 64 {
		return mutationError(codes.InvalidArgument, "invalid_delivery_token")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := o.store.pool.Begin(ctx)
	if err != nil {
		return mutationDatabaseError(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='1s'; SET LOCAL statement_timeout='1500ms'`); err != nil {
		return mutationDatabaseError(err)
	}
	var current, last string
	err = tx.QueryRow(ctx, `SELECT lease_token,last_ack_token FROM extension_p1_delivery WHERE installation=$1 AND instance=$2 AND consumer=$3 FOR UPDATE`, o.installation, o.instance, o.consumer).Scan(&current, &last)
	if err != nil {
		return mutationDatabaseError(err)
	}
	if last == token {
		return nil
	}
	if current != token {
		return mutationError(codes.FailedPrecondition, "delivery_lease_lost")
	}
	// Проверяем срок отдельным statement после получения блокировки строки.
	result, err := tx.Exec(ctx, `UPDATE extension_p1_delivery SET ack_revision=lease_revision,ack_ordinal=lease_ordinal,
 last_ack_token=lease_token,lease_token='',lease_until=NULL,attempts=0
 WHERE installation=$1 AND instance=$2 AND lease_until>clock_timestamp()`, o.installation, o.instance)
	if err != nil {
		return mutationDatabaseError(err)
	}
	if result.RowsAffected() != 1 {
		return mutationError(codes.FailedPrecondition, "delivery_lease_lost")
	}
	return mutationDatabaseError(tx.Commit(ctx))
}

// DispatchOne вызывает доверенный обработчик вне транзакции; ошибка оставляет
// событие для повторной доставки после срока аренды. Exactly-once не обещается.
// Возвращаемый bool означает начало попытки, а не успешное подтверждение.
func (o *ExtensionP1Outbox) DispatchOne(ctx context.Context, handle func(context.Context, ExtensionP1Delivery) error) (bool, error) {
	if handle == nil {
		return false, mutationError(codes.InvalidArgument, "missing_handler")
	}
	event, err := o.Claim(ctx)
	if err != nil || event == nil {
		return false, err
	}
	handlerCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = handle(handlerCtx, *event); err != nil {
		return true, err
	}
	return true, o.Ack(ctx, event.Token)
}
