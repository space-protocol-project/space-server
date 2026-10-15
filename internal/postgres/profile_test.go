package postgres

import (
	"context"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"strings"
	"testing"
)

func TestNicknameUniqueness(t *testing.T) {
	f := newChannelFixture(t)
	first, _ := f.enroll(t, true)
	second, _ := f.enroll(t, true)
	client := pb.NewMembershipServiceClient(f.conn)
	for _, pair := range [][2]string{{"Алиса", "  АЛИСА  "}, {"Alice", "ＡＬＩＣＥ"}, {"Café", "Cafe\u0301"}, {"Straße", "STRASSE"}, {"小明", " 小明 "}} {
		if _, err := client.UpdateProfile(first, &pb.UpdateProfileRequest{Nickname: pair[0]}); err != nil {
			t.Fatal(err)
		}
		_, err := client.UpdateProfile(second, &pb.UpdateProfileRequest{Nickname: pair[1]})
		requireCode(t, err, codes.AlreadyExists)
		if _, err := client.UpdateProfile(first, &pb.UpdateProfileRequest{Nickname: pair[1]}); err != nil {
			t.Fatal("Собственный ник не конфликтует", err)
		}
	}
	if _, err := client.UpdateProfile(first, &pb.UpdateProfileRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateProfile(second, &pb.UpdateProfileRequest{Nickname: "小明"}); err != nil {
		t.Fatal("Освобождённый ник недоступен", err)
	}
}

func TestConcurrentNicknameClaim(t *testing.T) {
	f := newChannelFixture(t)
	first, _ := f.enroll(t, true)
	second, _ := f.enroll(t, true)
	client := pb.NewMembershipServiceClient(f.conn)
	errors := make(chan error, 2)
	for _, actor := range []context.Context{first, second} {
		go func(actor context.Context) {
			_, err := client.UpdateProfile(actor, &pb.UpdateProfileRequest{Nickname: "Один ник"})
			errors <- err
		}(actor)
	}
	one, two := <-errors, <-errors
	if one == nil {
		requireCode(t, two, codes.AlreadyExists)
	} else {
		requireCode(t, one, codes.AlreadyExists)
		if two != nil {
			t.Fatal(two)
		}
	}
}

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
