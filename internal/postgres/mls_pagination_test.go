package postgres

import (
	"bytes"
	"slices"
	"testing"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
)

func TestMlsDevicePagesUseCanonicalIDOrder(t *testing.T) {
	f := newChannelFixture(t)
	owner, own := enrollMls(t, f, true)
	_, err := f.channels.CreateChannel(owner, &pb.CreateChannelRequest{ChannelId: "pages", Title: "Каталог", ViewType: "chat", MlsGroupId: bytes.Repeat([]byte{6}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{own.GrantId}
	for range 102 {
		_, device := enrollMls(t, f, false)
		expected = append(expected, device.GrantId)
	}
	slices.Sort(expected)
	rpc := pb.NewMlsServiceClient(f.conn)
	var received []string
	after := ""
	for range 3 {
		page, err := rpc.ListMlsDevices(owner, &pb.ListMlsDevicesRequest{ChannelId: "pages", After: after})
		if err != nil {
			t.Fatal(err)
		}
		for _, device := range page.Devices {
			received = append(received, device.GrantId)
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor <= after {
			t.Fatal("Курсор не продвинулся в ASCII-порядке")
		}
		after = page.NextCursor
	}
	if !slices.Equal(received, expected) {
		t.Fatal("Страницы зависят от locale, пропускают или повторяют устройства")
	}
}
