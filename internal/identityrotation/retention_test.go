package identityrotation

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestRetentionProofs(t *testing.T) {
	devices := make([]RetainedDevice, 3)
	for i := range devices {
		devices[i] = RetainedDevice{ExpiresAt: 1800000000 + int64(i), GrantID: "dg_" + strings.Repeat(string(rune('a'+i)), 43), PublicKey: encoded(bytes.Repeat([]byte{byte(i)}, 32)), Scopes: []string{"chat.read", "chat.write"}}
	}
	root, err := RetentionRoot(devices)
	if err != nil {
		t.Fatal(err)
	}
	if encoded(root) != "6nKeN-KvIx5zE1tapOJ3VUcTK2VC37hg4vVAR1OpShI" {
		t.Fatal("Изменился общий Go/Dart вектор")
	}
	for i := range devices {
		proof, err := ProveRetention(devices, i)
		if err != nil {
			t.Fatal(err)
		}
		if VerifyRetention(root, proof) != nil {
			t.Fatal("Верное доказательство отклонено")
		}
		forged := proof
		forged.Device.ExpiresAt++
		if VerifyRetention(root, forged) == nil {
			t.Fatal("Продление разрешения принято")
		}
		forged = proof
		forged.Index = (i + 1) % len(devices)
		if VerifyRetention(root, forged) == nil {
			t.Fatal("Перестановка принята")
		}
		forged = proof
		forged.Count++
		if VerifyRetention(root, forged) == nil {
			t.Fatal("Подмена размера списка принята")
		}
		forged = proof
		forged.Siblings = append(append([][]byte{}, proof.Siblings...), root)
		if VerifyRetention(root, forged) == nil {
			t.Fatal("Лишняя ветвь принята")
		}
	}
	devices[1] = devices[0]
	if _, err := RetentionRoot(devices); err == nil {
		t.Fatal("Дубликат принят")
	}
}

func TestRetentionDeviceLimit(t *testing.T) {
	devices := make([]RetainedDevice, 257)
	for i := range devices {
		devices[i] = RetainedDevice{ExpiresAt: 1800000000, GrantID: fmt.Sprintf("dg_%043d", i), PublicKey: encoded(bytes.Repeat([]byte{byte(i)}, 32)), Scopes: []string{"chat.read", "chat.write"}}
	}
	root, err := RetentionRoot(devices[:256])
	if err != nil {
		t.Fatal(err)
	}
	proof, err := ProveRetention(devices[:256], 255)
	if err != nil || len(proof.Siblings) != 8 || VerifyRetention(root, proof) != nil {
		t.Fatal("Группа предельного размера не проверяется", err)
	}
	if _, err = RetentionRoot(devices); err == nil {
		t.Fatal("Слишком большой список принят")
	}
	if _, err = RetentionRoot(nil); err == nil {
		t.Fatal("Пустой список принят")
	}
}
