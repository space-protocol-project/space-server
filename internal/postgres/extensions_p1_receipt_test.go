package postgres

import (
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/protobuf/proto"
	"testing"
)

func verifyExtensionCrashReceipt(t *testing.T, f *channelFixture, response *pb.ExecuteExtensionCommandResponse) {
	t.Helper()
	var raw []byte
	if err := f.store.pool.QueryRow(f.ctx, "SELECT response FROM extension_p1_receipts WHERE instance='demo'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	saved := new(pb.ExecuteExtensionCommandResponse)
	if err := proto.Unmarshal(raw, saved); err != nil {
		t.Fatal(err)
	}
	actual := proto.Clone(response).(*pb.ExecuteExtensionCommandResponse)
	actual.Replayed = false
	if !proto.Equal(saved, actual) {
		t.Fatal("Ответ не совпал с сохранённой квитанцией")
	}
}
