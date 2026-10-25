package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const recentMentionCount = `SELECT
 (SELECT count(*) FROM community_mentions WHERE author_id=$1 AND created_at>=extract(epoch FROM now())::bigint-3600)+
 (SELECT count(*) FROM personal_notifications WHERE kind=5 AND author_id=$1 AND created_at>=extract(epoch FROM now())::bigint-3600)`

func checkChatMentionBudget(ctx context.Context, tx pgx.Tx, channel, id string, mentions []*pb.CommunityMention) error {
	if len(mentions) == 0 {
		return nil
	}
	actor := authn.Actor(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "community-mentions:"+actor); err != nil {
		return databaseError(ctx, err)
	}
	recipients := map[string]bool{}
	for _, m := range mentions {
		if m.PrincipalId != actor {
			recipients[m.PrincipalId] = true
		}
	}
	rows, err := tx.Query(ctx, "SELECT recipient_id FROM personal_notifications WHERE channel_id=$1 AND kind=5 AND content_id=$2", channel, id)
	if err != nil {
		return databaseError(ctx, err)
	}
	for rows.Next() {
		var recipient string
		if err = rows.Scan(&recipient); err != nil {
			rows.Close()
			return databaseError(ctx, err)
		}
		delete(recipients, recipient)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return databaseError(ctx, err)
	}
	var recent int
	if err = tx.QueryRow(ctx, recentMentionCount, actor).Scan(&recent); err != nil {
		return databaseError(ctx, err)
	}
	if len(recipients) > 0 && recent+len(recipients) > 100 {
		return status.Error(codes.ResourceExhausted, "Лимит упоминаний за час исчерпан")
	}
	return nil
}
