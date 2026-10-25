package postgres

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
)

func mentionJSON(mentions []*pb.CommunityMention) []byte {
	if len(mentions) == 0 {
		return []byte("[]")
	}
	encoded, _ := json.Marshal(mentions)
	return encoded
}

// Один запрос для страницы, включая историю, поиск, события и идемпотентный ответ.
func populateChatMentions(ctx context.Context, tx pgx.Tx, contents []*pb.Content) error {
	if len(contents) == 0 {
		return nil
	}
	channels, ids := []string{}, []string{}
	targets := map[string]*pb.Content{}
	for _, content := range contents {
		content.Mentions = nil
		if content.Deleted {
			continue
		}
		channels = append(channels, content.ChannelId)
		ids = append(ids, content.Id)
		targets[content.ChannelId+"\x00"+content.Id] = content
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT c.channel_id,c.id,c.mentions FROM contents c
 JOIN unnest($1::text[],$2::text[]) AS q(channel_id,id) USING(channel_id,id) WHERE NOT c.deleted`, channels, ids)
	if err != nil {
		return databaseError(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		var channel, id string
		var encoded []byte
		if err = rows.Scan(&channel, &id, &encoded); err != nil {
			return databaseError(ctx, err)
		}
		if err = json.Unmarshal(encoded, &targets[channel+"\x00"+id].Mentions); err != nil {
			return databaseError(ctx, err)
		}
	}
	return databaseErrorOrNil(ctx, rows.Err())
}
