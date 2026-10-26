package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func favoriteState(ctx context.Context, tx pgx.Tx, channel string, ref *pb.CommunityContentReference) (*pb.CommunityFavoriteState, error) {
	out := &pb.CommunityFavoriteState{Content: ref}
	err := tx.QueryRow(ctx, `SELECT saved,revision FROM community_favorites WHERE principal_id=$1 AND channel_id=$2 AND kind=$3 AND target_id=$4`, authn.Actor(ctx), channel, int32(ref.Kind), ref.Id).Scan(&out.Saved, &out.Revision)
	if err == pgx.ErrNoRows {
		return out, nil
	}
	return out, databaseErrorOrNil(ctx, err)
}

func (s *Store) GetCommunityFavoriteStates(ctx context.Context, req *pb.GetCommunityFavoriteStatesRequest) (*pb.GetCommunityFavoriteStatesResponse, error) {
	if err := validateCommunityReferences(req.Contents); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, c, err := s.communityContentTransaction(ctx, req.ChannelId, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	out := new(pb.GetCommunityFavoriteStatesResponse)
	for _, ref := range req.Contents {
		if _, err = communityContentTarget(ctx, tx, c, ref); status.Code(err) == codes.NotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		state, err := favoriteState(ctx, tx, c.Id, ref)
		if err != nil {
			return nil, err
		}
		out.States = append(out.States, state)
	}
	return out, nil
}

const favoritePageSQL = `SELECT f.kind,f.target_id,c.parent_id,c.title,c.body,c.author_id,COALESCE(NULLIF(a.nickname,''),c.author_name),c.created_at,c.sequence,f.saved_at
 FROM community_favorites f JOIN community_mention_content c ON c.channel_id=f.channel_id AND c.kind=f.kind AND c.id=f.target_id
 LEFT JOIN memberships a ON a.principal_id=c.author_id
 WHERE f.principal_id=$1 AND f.channel_id=$2 AND f.saved
 AND (f.kind<=2)=$8
 AND (NOT $3 OR (f.saved_at,f.kind,f.target_id COLLATE "C") < ($4,$5,$6 COLLATE "C"))
 ORDER BY f.saved_at DESC,f.kind DESC,f.target_id COLLATE "C" DESC LIMIT $7`

func (s *Store) ListCommunityFavorites(ctx context.Context, req *pb.ListCommunityFavoritesRequest) (*pb.ListCommunityFavoritesResponse, error) {
	if req.Limit < 0 || req.Limit > 50 {
		return nil, status.Error(codes.InvalidArgument, "Нужна страница до 50 записей")
	}
	limit := int(req.Limit)
	if limit == 0 {
		limit = 20
	}
	binding := searchBinding(req.ChannelId, "community-favorites-v1", authn.Actor(ctx))
	cursor, err := parseCommunityCursor(req.PageToken, binding)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, c, err := s.communityContentTransaction(ctx, req.ChannelId, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, favoritePageSQL, authn.Actor(ctx), req.ChannelId, req.PageToken != "", cursor.Created, cursor.Kind, cursor.ID, limit+1, c.Views[0].Type == "forum")
	if err != nil {
		return nil, databaseError(ctx, err)
	}
	defer rows.Close()
	out := new(pb.ListCommunityFavoritesResponse)
	for rows.Next() {
		item := &pb.CommunityFavorite{Result: new(pb.CommunitySearchResult)}
		r := item.Result
		var body string
		if err = rows.Scan(&r.Kind, &r.Id, &r.ParentId, &r.Title, &body, &r.AuthorId, &r.AuthorName, &r.CreatedAtUnix, &r.Sequence, &item.SavedAtUnixMicros); err != nil {
			return nil, databaseError(ctx, err)
		}
		if len(out.Favorites) == limit {
			previous := out.Favorites[limit-1]
			out.NextPageToken = communityPageToken(&pb.CommunitySearchResult{Kind: previous.Result.Kind, Id: previous.Result.Id, CreatedAtUnix: previous.SavedAtUnixMicros}, binding)
			break
		}
		r.Snippet = communitySnippet(body, "")
		out.Favorites = append(out.Favorites, item)
	}
	return out, databaseErrorOrNil(ctx, rows.Err())
}
