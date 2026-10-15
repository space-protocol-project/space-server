package postgres

import (
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"strings"
	"testing"
)

func TestOwnNickname(t *testing.T) {
	f := newChannelFixture(t)
	member, id := f.enroll(t, true)
	client := pb.NewMembershipServiceClient(f.conn)
	result, err := client.UpdateProfile(member, &pb.UpdateProfileRequest{Nickname: "  Участник  "})
	if err != nil || result.Member.Nickname != "Участник" || result.Member.PrincipalId != id {
		t.Fatalf("Ник: %v %v", result, err)
	}
	listed, err := f.admin.ListMembers(f.owner, &pb.ListMembersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range listed.Members {
		if m.PrincipalId == id {
			found = m.Nickname == "Участник"
		}
	}
	if !found {
		t.Fatal("Ник отсутствует в карточке участника")
	}
	_, err = client.UpdateProfile(member, &pb.UpdateProfileRequest{Nickname: strings.Repeat("я", 41)})
	requireCode(t, err, codes.InvalidArgument)
	_, err = client.UpdateProfile(member, &pb.UpdateProfileRequest{Nickname: "строка\nстрока"})
	requireCode(t, err, codes.InvalidArgument)
	cleared, err := client.UpdateProfile(member, &pb.UpdateProfileRequest{})
	if err != nil || cleared.Member.Nickname != "" {
		t.Fatal("Не удалось убрать ник", err)
	}
}
