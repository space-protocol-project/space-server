package postgres

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func avatarFixture(t *testing.T, size int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	img.Set(0, 0, color.NRGBA{R: 180, G: 90, B: 20, A: 255})
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestParticipantAvatarValidation(t *testing.T) {
	good := avatarFixture(t, 16)
	if _, err := profileAvatar(good); err != nil {
		t.Fatal(err)
	}
	// Валидный служебный блок APNG, который png.Decode иначе игнорирует.
	animation := make([]byte, 20)
	binary.BigEndian.PutUint32(animation, 8)
	copy(animation[4:], "acTL")
	binary.BigEndian.PutUint32(animation[8:], 2)
	binary.BigEndian.PutUint32(animation[16:], crc32.ChecksumIEEE(animation[4:16]))
	apng := append(append(append([]byte{}, good[:33]...), animation...), good[33:]...)
	for _, raw := range [][]byte{nil, {}, []byte("<svg/>"), bytes.Repeat([]byte{1}, 8193), avatarFixture(t, 257), apng, append(append([]byte{}, good...), 1), good[:len(good)-1]} {
		_, err := profileAvatar(raw)
		requireCode(t, err, codes.InvalidArgument)
	}
}

func TestParticipantProfileRevisionAvatarPersistence(t *testing.T) {
	f := newChannelFixture(t)
	member, id := f.enroll(t, false)
	if _, err := f.admin.UpdateMember(f.owner, &pb.UpdateMemberRequest{PrincipalId: id, Role: "reader", ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}

	rpc := pb.NewMembershipServiceClient(f.conn)
	own, err := rpc.GetParticipantProfile(member, &pb.GetParticipantProfileRequest{})
	if err != nil || own.Profile.PrincipalId != id || own.Profile.Revision != 0 {
		t.Fatalf("Начальный профиль: %v %v", own, err)
	}
	owner, err := rpc.GetParticipantProfile(f.owner, &pb.GetParticipantProfileRequest{})
	if err != nil || owner.Profile.PrincipalId != f.ownerID {
		t.Fatal("Профиль владельца", err)
	}
	avatar := avatarFixture(t, 16)
	saved, err := rpc.UpdateParticipantProfile(member, &pb.UpdateParticipantProfileRequest{DisplayName: "  Алиса  ", AvatarPng: avatar})
	if err != nil || saved.Profile.DisplayName != "Алиса" || saved.Profile.Revision != 1 || len(saved.Profile.AvatarPng) == 0 {
		t.Fatalf("Сохранение: %v %v", saved, err)
	}
	canonical := saved.Profile.AvatarPng
	renamed, err := rpc.UpdateParticipantProfile(member, &pb.UpdateParticipantProfileRequest{DisplayName: "Новое имя", ExpectedRevision: saved.Profile.Revision})
	if err != nil || renamed.Profile.PrincipalId != id {
		t.Fatal(err)
	}
	current, err := rpc.GetParticipantProfile(member, &pb.GetParticipantProfileRequest{})
	if err != nil || current.Profile.Revision != 2 || !bytes.Equal(current.Profile.AvatarPng, canonical) {
		t.Fatal("Изменение имени потеряло аватар", err)
	}
	_, err = rpc.UpdateParticipantProfile(member, &pb.UpdateParticipantProfileRequest{DisplayName: "Конфликт", RemoveAvatar: true, ExpectedRevision: 1})
	requireCode(t, err, codes.Aborted)
	_, err = rpc.UpdateParticipantProfile(member, &pb.UpdateParticipantProfileRequest{DisplayName: "Неверный", AvatarPng: []byte("bad"), ExpectedRevision: 2})
	requireCode(t, err, codes.InvalidArgument)
	again, _ := rpc.GetParticipantProfile(member, &pb.GetParticipantProfileRequest{})
	if again.Profile.DisplayName != "Новое имя" || again.Profile.Revision != 2 || !bytes.Equal(again.Profile.AvatarPng, canonical) {
		t.Fatal("Неатомарная ошибка")
	}
	reopened, identity, err := openManual(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var restored []byte
	if err = reopened.pool.QueryRow(f.ctx, "SELECT profile_avatar FROM memberships WHERE principal_id=$1", id).Scan(&restored); err != nil || !bytes.Equal(restored, canonical) || identity.ServerID != f.identity.ServerID {
		t.Fatal("Профиль/сервер потерян при перезапуске", err)
	}
	removed, err := rpc.UpdateParticipantProfile(member, &pb.UpdateParticipantProfileRequest{DisplayName: "Новое имя", RemoveAvatar: true, ExpectedRevision: 2})
	if err != nil || len(removed.Profile.AvatarPng) != 0 || removed.Profile.Revision != 3 {
		t.Fatal("Удаление аватара", err)
	}
	var role string
	if err = f.store.pool.QueryRow(f.ctx, "SELECT role FROM memberships WHERE principal_id=$1", id).Scan(&role); err != nil || role != "reader" {
		t.Fatal("Изменена роль", err)
	}
}

func TestParticipantProfileUniqueNames(t *testing.T) {
	f := newChannelFixture(t)
	first, _ := f.enroll(t, true)
	second, _ := f.enroll(t, true)
	rpc := pb.NewMembershipServiceClient(f.conn)
	for _, pair := range [][2]string{{"Алиса", "АЛИСА"}, {"Alice", "ＡＬＩＣＥ"}, {"Café", "Cafe\u0301"}, {"Straße", "STRASSE"}, {"小明", " 小明 "}} {
		current, err := rpc.GetParticipantProfile(first, &pb.GetParticipantProfileRequest{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = rpc.UpdateParticipantProfile(first, &pb.UpdateParticipantProfileRequest{DisplayName: pair[0], ExpectedRevision: current.Profile.Revision})
		if err != nil {
			t.Fatal(err)
		}
		_, err = rpc.UpdateParticipantProfile(second, &pb.UpdateParticipantProfileRequest{DisplayName: pair[1]})
		requireCode(t, err, codes.AlreadyExists)
	}
	for _, name := range []string{strings.Repeat("я", 41), "имя\nстрока", "\u202eимя"} {
		_, err := rpc.UpdateParticipantProfile(second, &pb.UpdateParticipantProfileRequest{DisplayName: name})
		requireCode(t, err, codes.InvalidArgument)
	}
}

func TestParticipantConcurrentNameClaim(t *testing.T) {
	f := newChannelFixture(t)
	first, _ := f.enroll(t, true)
	second, _ := f.enroll(t, true)
	rpc := pb.NewMembershipServiceClient(f.conn)
	results := make(chan error, 2)
	go func() {
		_, err := rpc.UpdateParticipantProfile(first, &pb.UpdateParticipantProfileRequest{DisplayName: "Общее имя"})
		results <- err
	}()
	go func() {
		_, err := rpc.UpdateParticipantProfile(second, &pb.UpdateParticipantProfileRequest{DisplayName: "ОБЩЕЕ ИМЯ"})
		results <- err
	}()
	one, two := <-results, <-results
	if one == nil {
		requireCode(t, two, codes.AlreadyExists)
	} else {
		requireCode(t, one, codes.AlreadyExists)
		if two != nil {
			t.Fatal(two)
		}
	}
}

func TestParticipantProfilesChannelRightsAndBlocking(t *testing.T) {
	f := newChannelFixture(t)
	member, id := f.enroll(t, false)
	other, otherID := f.enroll(t, false)
	channel := f.create(t, "profiles")
	f.acl(t, channel, roleRule("member", permissions(true, true, false, false)), principalRule(otherID, permissions(false, false, false, false)))
	rpc := pb.NewMembershipServiceClient(f.conn)
	result, err := rpc.ListParticipantProfiles(member, &pb.ListParticipantProfilesRequest{ChannelId: channel.Id, PrincipalIds: []string{id, otherID, "unknown"}})
	if err != nil || len(result.Profiles) != 1 || result.Profiles[0].PrincipalId != id {
		t.Fatal("Раскрыт скрытый участник", result, err)
	}
	_, err = rpc.ListParticipantProfiles(other, &pb.ListParticipantProfilesRequest{ChannelId: channel.Id, PrincipalIds: []string{id}})
	requireCode(t, err, codes.NotFound)
	_, err = rpc.GetParticipantProfile(context.Background(), &pb.GetParticipantProfileRequest{})
	requireCode(t, err, codes.Unauthenticated)
	_, err = rpc.ListParticipantProfiles(member, &pb.ListParticipantProfilesRequest{ChannelId: channel.Id, PrincipalIds: make([]string, 51)})
	requireCode(t, err, codes.InvalidArgument)
	_, err = f.admin.UpdateMember(f.owner, &pb.UpdateMemberRequest{PrincipalId: id, Role: "member", Blocked: true, ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = rpc.GetParticipantProfile(member, &pb.GetParticipantProfileRequest{})
	if status.Code(err) != codes.PermissionDenied && status.Code(err) != codes.Unauthenticated {
		t.Fatal("Заблокированный участник получил профиль", err)
	}
	result, err = rpc.ListParticipantProfiles(f.owner, &pb.ListParticipantProfilesRequest{ChannelId: channel.Id, PrincipalIds: []string{id}})
	if err != nil || len(result.Profiles) != 0 {
		t.Fatal("Возвращён профиль заблокированного", err)
	}
}

func TestParticipantProfilesMlsRoster(t *testing.T) {
	f := newChannelFixture(t)
	alice, a := enrollMls(t, f, true)
	_, b := enrollMls(t, f, false)
	_, err := f.channels.CreateChannel(alice, &pb.CreateChannelRequest{ChannelId: "profile_mls", Title: "Закрытый", ViewType: "chat", MlsGroupId: bytes.Repeat([]byte{33}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	outsider, _ := enrollMls(t, f, true)

	rpc := pb.NewMembershipServiceClient(f.conn)
	result, err := rpc.ListParticipantProfiles(alice, &pb.ListParticipantProfilesRequest{ChannelId: "profile_mls", PrincipalIds: []string{a.PrincipalId, b.PrincipalId}})
	if err != nil || len(result.Profiles) != 1 || result.Profiles[0].PrincipalId != a.PrincipalId {
		t.Fatal("Раскрыт участник вне MLS-состава", result, err)
	}
	_, err = rpc.ListParticipantProfiles(outsider, &pb.ListParticipantProfilesRequest{ChannelId: "profile_mls", PrincipalIds: []string{a.PrincipalId}})
	requireCode(t, err, codes.PermissionDenied)

}
