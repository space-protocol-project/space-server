// Package push определяет приватный формат сигнала независимо от службы доставки.
package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"
)

const envelopeSize = 1024

var ErrEnvelope = errors.New("недопустимый push-конверт")

// Target не содержит текста, имени отправителя, названия канала или MLS-ключей.
// Binding — случайный идентификатор локально сохранённой привязки устройства.
type Target struct {
	Group    string `json:"group,omitempty"`
	Binding  string `json:"binding"`
	Channel  string `json:"channel"`
	Sequence int64  `json:"sequence"`
	Event    string `json:"event"`
	Expires  int64  `json:"expires"`
}

func (t Target) valid(now time.Time) bool {
	return validID(t.Binding) && validID(t.Event) &&
		len(t.Channel) > 0 && len(t.Channel) <= 128 && t.Sequence > 0 &&
		t.Expires > now.Unix() && t.Expires <= now.Add(24*time.Hour).Unix()
}

func validID(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func aead(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, ErrEnvelope
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal использует отдельный ключ push-привязки, никогда не ключ MLS.
// Фиксированный размер скрывает длину идентификаторов от службы доставки.
func Seal(key []byte, target Target, now time.Time) (string, error) {
	if !target.valid(now) {
		return "", ErrEnvelope
	}
	box, err := aead(key)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(target)
	if err != nil || len(encoded) > envelopeSize-2 {
		return "", ErrEnvelope
	}
	plain := make([]byte, envelopeSize)
	plain[0], plain[1] = byte(len(encoded)>>8), byte(len(encoded))
	copy(plain[2:], encoded)
	nonce := make([]byte, box.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := box.Seal(nonce, nonce, plain, []byte("space.push.v1"))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open предназначен также для совместимых тестов; доступ проверяется после
// расшифровки через авторизованный сервер. Конверт сам по себе не даёт доступа.
func Open(key []byte, envelope string, now time.Time) (Target, error) {
	var target Target
	box, err := aead(key)
	if err != nil {
		return target, err
	}
	if len(envelope) > 1500 {
		return target, ErrEnvelope
	}
	raw, err := base64.RawURLEncoding.DecodeString(envelope)
	if err != nil || len(raw) != box.NonceSize()+envelopeSize+box.Overhead() {
		return target, ErrEnvelope
	}
	plain, err := box.Open(nil, raw[:box.NonceSize()], raw[box.NonceSize():], []byte("space.push.v1"))
	if err != nil {
		return target, ErrEnvelope
	}
	size := int(plain[0])<<8 | int(plain[1])
	if size > len(plain)-2 {
		return target, ErrEnvelope
	}
	if json.Unmarshal(plain[2:2+size], &target) != nil || !target.valid(now) {
		return Target{}, ErrEnvelope
	}
	return target, nil
}
