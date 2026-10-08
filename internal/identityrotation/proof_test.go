package identityrotation

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func TestRotationProof(t *testing.T) {
	oldPub, oldPrivate, _ := ed25519.GenerateKey(rand.Reader)
	newPub, newPrivate, _ := ed25519.GenerateKey(rand.Reader)
	device, _, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1791440000, 0)
	transcript := Transcript{
		AuthEpoch: 7, ChallengeID: "rc_" + strings.Repeat("a", 43), ExpiresAt: now.Unix() + 120, IssuedAt: now.Unix(),
		NewDevicePublicKey: encoded(device), NewRootPublicKey: encoded(newPub), Nonce: strings.Repeat("b", 43),
		OldRootPublicKey: encoded(oldPub), OperationID: "ro_" + strings.Repeat("c", 43), Origin: "http://127.0.0.1:8080",
		PrincipalID: "u_" + strings.Repeat("d", 64), Purpose: "identity.root.rotate", Scopes: []string{"chat.read", "chat.write"},
		ServerID: "srv_" + strings.Repeat("e", 32), V: 1,
	}
	expected := Expected{PrincipalID: transcript.PrincipalID, RootPublicKey: oldPub, AuthEpoch: 7, ServerID: transcript.ServerID,
		Origin: transcript.Origin, ChallengeID: transcript.ChallengeID, OperationID: transcript.OperationID,
		NewRootPublicKey: newPub, NewDevicePublicKey: device, Scopes: transcript.Scopes}
	oldBytes, _ := transcript.SigningBytes(false)
	newBytes, _ := transcript.SigningBytes(true)
	oldSignature := ed25519.Sign(oldPrivate, oldBytes)
	newSignature := ed25519.Sign(newPrivate, newBytes)
	if err := transcript.Verify(expected, now, oldSignature, newSignature); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Transcript){
		"чужой principal":          func(v *Transcript) { v.PrincipalID = "u_" + strings.Repeat("f", 64) },
		"чужой origin":             func(v *Transcript) { v.Origin = "http://127.0.0.1:8081" },
		"другая эпоха":             func(v *Transcript) { v.AuthEpoch++ },
		"подмена устройства":       func(v *Transcript) { v.NewDevicePublicKey = encoded(oldPub) },
		"подмена операции":         func(v *Transcript) { v.OperationID = "ro_" + strings.Repeat("z", 43) },
		"scope escalation":         func(v *Transcript) { v.Scopes = []string{"chat.read", "chat.write", "space.manage"} },
		"повтор старого root":      func(v *Transcript) { v.NewRootPublicKey = v.OldRootPublicKey },
		"истечение":                func(v *Transcript) { v.ExpiresAt = now.Unix() },
		"слишком долгий challenge": func(v *Transcript) { v.ExpiresAt++ },
	} {
		t.Run(name, func(t *testing.T) {
			altered := transcript
			mutate(&altered)
			if altered.Verify(expected, now, oldSignature, newSignature) == nil {
				t.Fatal("Подмена принята")
			}
		})
	}
	t.Run("новый root без possession", func(t *testing.T) {
		if transcript.Verify(expected, now, oldSignature, oldSignature) == nil {
			t.Fatal("Нет подтверждения нового root")
		}
	})
	t.Run("root и device должны быть разными", func(t *testing.T) {
		altered := transcript
		altered.NewDevicePublicKey = altered.NewRootPublicKey
		bound := expected
		bound.NewDevicePublicKey = newPub
		oldBytes, _ := altered.SigningBytes(false)
		newBytes, _ := altered.SigningBytes(true)
		if altered.Verify(bound, now, ed25519.Sign(oldPrivate, oldBytes), ed25519.Sign(newPrivate, newBytes)) == nil {
			t.Fatal("Один ключ получил root и device authority")
		}
	})
	t.Run("разделение доменов", func(t *testing.T) {
		wrong := ed25519.Sign(newPrivate, oldBytes)
		if transcript.Verify(expected, now, oldSignature, wrong) == nil {
			t.Fatal("Перепутанные домены приняты")
		}
	})
	t.Run("старый challenge после срока", func(t *testing.T) {
		if transcript.Verify(expected, now.Add(121*time.Second), oldSignature, newSignature) == nil {
			t.Fatal("Истёкший challenge принят")
		}
	})
}
