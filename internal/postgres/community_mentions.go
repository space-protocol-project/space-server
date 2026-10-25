package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const mentionMembers = ` FROM memberships m
 LEFT JOIN channel_access personal ON personal.channel_id=$1 AND personal.principal_id=m.principal_id
 LEFT JOIN channel_access role_acl ON role_acl.channel_id=$1 AND role_acl.role=m.role
 WHERE NOT m.blocked AND EXISTS(SELECT 1 FROM channels mention_channel WHERE mention_channel.id=$1 AND
 (mention_channel.mls_group_id IS NULL OR EXISTS(SELECT 1 FROM mls_members mm JOIN device_grants dg ON dg.id=mm.grant_id
 WHERE mm.channel_id=$1 AND mm.left_sequence IS NULL AND dg.principal_id=m.principal_id AND dg.revoked_at IS NULL)))
 AND (m.role IN ('owner','admin') OR
 (COALESCE(personal.visible,role_acl.visible,false) AND COALESCE(personal.can_read,role_acl.can_read,false)))`

func mentionName(name, principal string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return -1
		}
		return r
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || len(name) > 128 {
		return principal
	}
	return name
}

func invalidMention() error {
	return status.Error(codes.InvalidArgument, "Упоминание недоступно или изменено")
}

func mentionSpan(units []uint16, m *pb.CommunityMention, previous int) bool {
	if m == nil || m.Start < int32(previous) || m.Length < 2 || int64(m.Start)+int64(m.Length) > int64(len(units)) {
		return false
	}
	start, end := int(m.Start), int(m.Start)+int(m.Length)
	if start < 0 || (start < len(units) && units[start] >= 0xdc00 && units[start] <= 0xdfff) {
		return false
	}
	if end < len(units) && units[end] >= 0xdc00 && units[end] <= 0xdfff {
		return false
	}
	return string(utf16.Decode(units[start:end])) == "@"+m.DisplayName
}

func validateCommunityMentions(ctx context.Context, tx pgx.Tx, channel, text string, mentions []*pb.CommunityMention) ([]byte, error) {
	if len(mentions) > 10 {
		return nil, invalidMention()
	}
	units := utf16.Encode([]rune(text))
	end := 0
	for _, m := range mentions {
		if !mentionSpan(units, m, end) {
			return nil, invalidMention()
		}
		var name string
		err := tx.QueryRow(ctx, "SELECT m.nickname"+mentionMembers+" AND m.principal_id=$2", channel, m.PrincipalId).Scan(&name)
		if err == pgx.ErrNoRows {
			return nil, invalidMention()
		}
		if err != nil {
			return nil, databaseError(ctx, err)
		}
		if mentionName(name, m.PrincipalId) != m.DisplayName {
			return nil, invalidMention()
		}
		end = int(m.Start + m.Length)
	}
	if len(mentions) == 0 {
		return []byte("[]"), nil
	}
	encoded, err := json.Marshal(mentions)
	return encoded, err
}

func sameCommunityMentions(a, b []*pb.CommunityMention) bool {
	if len(a) != len(b) {
		return false
	}
	for i, m := range a {
		n := b[i]
		if m == nil || n == nil || m.PrincipalId != n.PrincipalId || m.DisplayName != n.DisplayName || m.Start != n.Start || m.Length != n.Length {
			return false
		}
	}
	return true
}

func mentionColumn(kind pb.CommunitySearchKind) string {
	switch kind {
	case pb.CommunitySearchKind_COMMUNITY_SEARCH_KIND_FORUM_THREAD:
		return "forum_thread_id"
	case pb.CommunitySearchKind_COMMUNITY_SEARCH_KIND_FORUM_REPLY:
		return "forum_reply_id"
	case pb.CommunitySearchKind_COMMUNITY_SEARCH_KIND_FEED_POST:
		return "feed_post_id"
	case pb.CommunitySearchKind_COMMUNITY_SEARCH_KIND_FEED_COMMENT:
		return "feed_comment_id"
	default:
		return ""
	}
}

func recordCommunityMentions(ctx context.Context, tx pgx.Tx, channel, id string, kind pb.CommunitySearchKind, mentions []*pb.CommunityMention) error {
	recipients := map[string]bool{}
	for _, m := range mentions {
		if m.PrincipalId != authn.Actor(ctx) {
			recipients[m.PrincipalId] = true
		}
	}
	if len(recipients) == 0 {
		return nil
	}
	// Одна личность имеет общий бюджет во всех разделах и устройствах.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "community-mentions:"+authn.Actor(ctx)); err != nil {
		return databaseError(ctx, err)
	}
	var recent int
	if err := tx.QueryRow(ctx, recentMentionCount, authn.Actor(ctx)).Scan(&recent); err != nil {
		return databaseError(ctx, err)
	}
	if recent+len(recipients) > 100 {
		return status.Error(codes.ResourceExhausted, "Лимит упоминаний за час исчерпан")
	}
	column := mentionColumn(kind)
	for recipient := range recipients {
		if _, err := tx.Exec(ctx, "INSERT INTO community_mentions(channel_id,author_id,recipient_id,"+column+") VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", channel, authn.Actor(ctx), recipient, id); err != nil {
			return databaseError(ctx, err)
		}
	}
	return nil
}
