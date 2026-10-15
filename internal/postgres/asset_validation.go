package postgres

import (
	"crypto/sha256"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const assetChunkSize = 256 * 1024
const assetMaxSize = 20 * 1024 * 1024

func validateUpload(req *pb.BeginUploadRequest) error {
	if req == nil || req.ChannelId == "" {
		return status.Error(codes.InvalidArgument, "Укажите канал")
	}
	if !validAssetName(req.Name) {
		return status.Error(codes.InvalidArgument, "Некорректное имя файла")
	}
	maximum := int64(assetMaxSize)
	if req.Encrypted {
		maximum += 16
	}
	if req.ByteSize < 1 || req.ByteSize > maximum {
		return status.Error(codes.InvalidArgument, "Размер файла должен быть от 1 байта до 20 МиБ")
	}
	if len(req.Sha256) != sha256.Size {
		return status.Error(codes.InvalidArgument, "Требуется SHA256 файла")
	}
	if req.IdempotencyKey == "" || len(req.IdempotencyKey) > 128 {
		return status.Error(codes.InvalidArgument, "Некорректный ключ загрузки")
	}
	return nil
}

func validAssetName(name string) bool {
	if !utf8.ValidString(name) || strings.TrimSpace(name) == "" || len(name) > 255 {
		return false
	}
	if name == "." || name == ".." || path.Base(name) != name || strings.ContainsAny(name, `\/:`) {
		return false
	}
	return strings.IndexFunc(name, unicode.IsControl) < 0
}

func validateAssetChunk(offset int64, data []byte, size int64) error {
	if offset < 0 || offset > size || offset%assetChunkSize != 0 {
		return status.Error(codes.InvalidArgument, "Некорректное смещение файла")
	}
	remaining := size - offset
	expected := min(int64(assetChunkSize), remaining)
	if expected == 0 || int64(len(data)) != expected {
		return status.Error(codes.InvalidArgument, "Некорректный размер части файла")
	}
	return nil
}
