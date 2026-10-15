package postgres

import (
	"crypto/sha256"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"strings"
	"testing"
)

func TestAssetNames(t *testing.T) {
	for _, name := range []string{"фото.png", "report 2026.pdf", "archive.tar.gz"} {
		if !validAssetName(name) {
			t.Errorf("Отклонено допустимое имя %q", name)
		}
	}
	for _, name := range []string{"", " ", ".", "..", "../secret", `C:\secret`, "a/b", "a:b", "a\x00b", "a\nb", strings.Repeat("я", 128), string([]byte{255})} {
		if validAssetName(name) {
			t.Errorf("Принято опасное имя %q", name)
		}
	}
}

func TestUploadLimits(t *testing.T) {
	valid := func() *pb.BeginUploadRequest {
		return &pb.BeginUploadRequest{ChannelId: "general", Name: "file", ByteSize: assetMaxSize, Sha256: make([]byte, sha256.Size), IdempotencyKey: "key"}
	}
	if err := validateUpload(valid()); err != nil {
		t.Fatal(err)
	}
	cases := []func(*pb.BeginUploadRequest){
		func(r *pb.BeginUploadRequest) { r.ByteSize = assetMaxSize + 1 },
		func(r *pb.BeginUploadRequest) { r.ByteSize = 0 },
		func(r *pb.BeginUploadRequest) { r.Sha256 = nil },
		func(r *pb.BeginUploadRequest) { r.IdempotencyKey = "" },
		func(r *pb.BeginUploadRequest) { r.ChannelId = "" },
	}
	for i, change := range cases {
		r := valid()
		change(r)
		if validateUpload(r) == nil {
			t.Errorf("Принят некорректный запрос %d", i)
		}
	}
	if validateUpload(nil) == nil {
		t.Fatal("Принят пустой запрос")
	}
}

func TestAssetChunkBoundaries(t *testing.T) {
	size := int64(assetChunkSize + 3)
	if err := validateAssetChunk(0, make([]byte, assetChunkSize), size); err != nil {
		t.Fatal(err)
	}
	if err := validateAssetChunk(assetChunkSize, []byte{1, 2, 3}, size); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int64{-1, 1, size, size + 1, 1 << 62} {
		if validateAssetChunk(offset, []byte{1}, size) == nil {
			t.Errorf("Принято смещение %d", offset)
		}
	}
	if validateAssetChunk(0, make([]byte, assetChunkSize+1), size) == nil {
		t.Fatal("Принята слишком большая часть")
	}
	if validateAssetChunk(0, []byte{1}, size) == nil {
		t.Fatal("Принята неполная промежуточная часть")
	}
}
