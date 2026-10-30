package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/space-protocol-project/space-server/experiments/extensions/demo"
	"github.com/space-protocol-project/space-server/internal/authn"
)

// ExtensionP1Authorizer получает привязки instance → channel только от ядра.
// Установка пакетов и сохранение реестра пока не подключены.
func (s *Store) ExtensionP1Authorizer(bindings map[string]string) demo.Authorize {
	bound := make(map[string]string, len(bindings))
	for instance, channel := range bindings {
		bound[instance] = channel
	}
	return func(ctx context.Context, token, instance string, command bool) (demo.Principal, error) {
		channel, ok := bound[instance]
		if !ok || token == "" || token != authn.Token(ctx) {
			return demo.Principal{}, denied()
		}
		return s.extensionP1Principal(ctx, channel, instance, command)
	}
}

func (s *Store) extensionP1Principal(ctx context.Context, channel, instance string, command bool) (demo.Principal, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var p demo.Principal
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	// Запрещаем внутренний demo-обход, push-only и подмену actor в context.
	hash := sha256.Sum256([]byte(authn.Token(ctx)))
	query := strings.Replace(sessionQuery, "SELECT p.id", "SELECT p.id,g.id,p.auth_epoch", 1)
	var epoch int64
	err = tx.QueryRow(ctx, query+" AND NOT s.push_only FOR SHARE OF s,g,p", hash[:]).Scan(&p.User, &p.Device, &epoch)
	if err != nil || p.User != authn.Actor(ctx) {
		return p, denied()
	}
	action := "read"
	if command {
		action = "write"
	}
	c, err := channelAccess(ctx, tx, channel, action, true)
	if err != nil {
		return p, err
	}
	m, err := membership(ctx, tx, p.User, true)
	if err != nil {
		return p, err
	}
	var settings int64
	err = tx.QueryRow(ctx, "SELECT revision FROM space_settings WHERE singleton=true").Scan(&settings)
	if err != nil {
		return p, err
	}
	err = tx.QueryRow(ctx, "SELECT server_id FROM server_state WHERE singleton=true").Scan(&p.Space)
	if err != nil {
		return p, err
	}
	scopes, err := deviceScopes(ctx, tx)
	if err != nil {
		return p, err
	}
	snapshot, _ := json.Marshal([]any{epoch, settings, m.Revision, m.Role, c.Revision, scopes})
	stamp := sha256.Sum256(snapshot)
	p.AccessStamp = hex.EncodeToString(stamp[:])
	p.Instance, p.Revision = instance, strconv.FormatInt(c.Revision, 10)
	if err = tx.Commit(ctx); err != nil {
		return demo.Principal{}, err
	}
	return p, nil
}
