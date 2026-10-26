package postgres

import (
    "context"
    "github.com/jackc/pgx/v5"
    pb "github.com/space-protocol-project/space-server/gen/space/v1"
    "github.com/space-protocol-project/space-server/internal/authn"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"
)

type spaceSearchChannels struct { chat, forum, feed []string }

func (s *Store) spaceSearchTransaction(ctx context.Context) (pgx.Tx, spaceSearchChannels, error) {
    var channels spaceSearchChannels
    if authn.Token(ctx) == "" { return nil, channels, status.Error(codes.Unauthenticated, "Нужна проверенная сессия") }
    tx, err := s.pool.Begin(ctx)
    if err != nil { return nil, channels, databaseError(ctx, err) }
    if err = s.lockSession(ctx, tx); err == nil { channels, err = readableSearchChannels(ctx, tx) }
    if err != nil { tx.Rollback(ctx); return nil, channels, err }
    return tx, channels, nil
}

func readableSearchChannels(ctx context.Context, tx pgx.Tx) (spaceSearchChannels, error) {
    var result spaceSearchChannels
    member, err := channelMember(ctx, tx, true)
    if err != nil { return result, err }
    if err = enabledChat(ctx, tx, true); err != nil { return result, err }
    scopes, err := deviceScopes(ctx, tx)
    if err != nil { return result, err }
    if !hasScope(scopes, "chat.read") { return result, forbidden() }
    candidates, err := lockedSearchChannels(ctx, tx)
    if err != nil { return result, err }
    for _, channel := range candidates {
        permissions, err := effectivePermissions(ctx, tx, channel.Id, member)
        if err != nil { return result, err }
        if !permissions.Visible || !permissions.Read { continue }
        switch channel.Views[0].Type {
        case "chat": result.chat = append(result.chat, channel.Id)
        case "forum": result.forum = append(result.forum, channel.Id)
        case "feed": result.feed = append(result.feed, channel.Id)
        }
    }
    return result, nil
}

// Единый порядок блокировок не зависит от пользовательской сортировки каталога.
func lockedSearchChannels(ctx context.Context, tx pgx.Tx) ([]*pb.Channel, error) {
    rows, err := tx.Query(ctx, `SELECT `+channelColumns+` FROM channels
 WHERE NOT archived AND mls_group_id IS NULL AND view_type IN ('chat','forum','feed')
 ORDER BY id COLLATE "C" FOR SHARE`)
    if err != nil { return nil, databaseError(ctx, err) }
    defer rows.Close()
    var channels []*pb.Channel
    for rows.Next() {
        channel, err := scanChannel(rows)
        if err != nil { return nil, databaseError(ctx, err) }
        channels = append(channels, channel)
    }
    return channels, databaseErrorOrNil(ctx, rows.Err())
}
