package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
)

// Возвращаем имена только тех, кто уже присутствует в доступных сообщениях.
func populateContentNames(ctx context.Context, tx pgx.Tx, contents []*pb.Content) error {
	ids := map[string]bool{}
	for _, content := range contents {
		ids[content.AuthorId] = true
		for _, reaction := range content.Reactions {
			for _, id := range reaction.PrincipalIds {
				ids[id] = true
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	keys := make([]string, 0, len(ids))
	for id := range ids {
		keys = append(keys, id)
	}
	rows, err := tx.Query(ctx, "SELECT principal_id,nickname FROM memberships WHERE principal_id=ANY($1) AND nickname<>''", keys)
	if err != nil {
		return databaseError(ctx, err)
	}
	defer rows.Close()
	names := map[string]string{}
	for rows.Next() {
		var id, name string
		if err = rows.Scan(&id, &name); err != nil {
			return databaseError(ctx, err)
		}
		names[id] = name
	}
	if err = rows.Err(); err != nil {
		return databaseError(ctx, err)
	}
	for _, content := range contents {
		content.ParticipantNames = namesForContent(content, names)
	}
	return nil
}

func namesForContent(content *pb.Content, names map[string]string) map[string]string {
	result := map[string]string{}
	if name := names[content.AuthorId]; name != "" {
		result[content.AuthorId] = name
	}
	for _, reaction := range content.Reactions {
		for _, id := range reaction.PrincipalIds {
			if name := names[id]; name != "" {
				result[id] = name
			}
		}
	}
	return result
}
