package postgres

import (
	"bytes"
	"encoding/binary"
	"image/png"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func profileAvatar(data []byte) ([]byte, error) {
	invalid := func() ([]byte, error) {
		return nil, status.Error(codes.InvalidArgument, "Нужен PNG-аватар до 8 КиБ и 256×256 пикселей")
	}
	if len(data) == 0 || len(data) > 8192 || !staticProfilePNG(data) {
		return invalid()
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 256 || config.Height > 256 {
		return invalid()
	}
	image, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return invalid()
	}
	var output bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err = encoder.Encode(&output, image); err != nil || output.Len() > 8192 {
		return invalid()
	}
	return output.Bytes(), nil
}

// Не допускаем анимацию и данные после IEND: png.Decode читает только первый кадр.
func staticProfilePNG(data []byte) bool {
	offset := 8
	for count := 0; count < 128; count++ {
		if offset+12 > len(data) {
			return false
		}
		size := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		if size > len(data)-offset-12 {
			return false
		}
		kind := string(data[offset+4 : offset+8])
		if kind == "acTL" || kind == "fcTL" || kind == "fdAT" {
			return false
		}
		offset += size + 12
		if kind == "IEND" {
			return size == 0 && offset == len(data)
		}
	}
	return false
}
