package push

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestEnvelopePrivacyAndAuthentication(t *testing.T) {
	now := time.Unix(1800000000, 0)
	key := bytes.Repeat([]byte{7}, 32)
	target := Target{Binding: strings.Repeat("a", 32), Channel: "private-channel", Sequence: 42, Event: strings.Repeat("b", 32), Expires: now.Add(time.Hour).Unix()}
	first, err := Seal(key, target, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Seal(key, target, now)
	if err != nil || first == second {
		t.Fatal("nonce повторён", err)
	}
	decoded, err := Open(key, first, now)
	if err != nil || decoded != target {
		t.Fatal(decoded, err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(first)
	if bytes.Contains(raw, []byte(target.Channel)) {
		t.Fatal("идентификатор раскрыт")
	}
	raw[len(raw)-1] ^= 1
	if _, err := Open(key, base64.RawURLEncoding.EncodeToString(raw), now); err == nil {
		t.Fatal("подмена принята")
	}
	if _, err := Open(bytes.Repeat([]byte{8}, 32), first, now); err == nil {
		t.Fatal("чужой ключ принят")
	}
	if _, err := Open(key, first, now.Add(time.Hour)); err == nil {
		t.Fatal("истёкший сигнал принят")
	}
	target.Channel = "x"
	short, _ := Seal(key, target, now)
	if len(short) != len(first) {
		t.Fatal("размер раскрывает длину канала")
	}
}

func TestRejectMalformedEnvelope(t *testing.T) {
	now := time.Now()
	key := make([]byte, 32)
	for _, value := range []string{"", "invalid", strings.Repeat("a", 10000)} {
		if _, err := Open(key, value, now); err == nil {
			t.Fatal("принят некорректный конверт")
		}
	}
	if _, err := Seal(key, Target{}, now); err == nil {
		t.Fatal("принята пустая цель")
	}
	if _, err := Seal(key[:16], Target{}, now); err == nil {
		t.Fatal("принят короткий ключ")
	}
}
