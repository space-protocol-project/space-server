package authn

import (
	"bytes"
	"testing"
)

func TestCanonicalAndDomainSeparation(t *testing.T) {
	transcript := Transcript{AuthEpoch: 1, ChallengeID: "ac_test", DevicePublicKey: "device", ExpiresAt: 160, GrantExpiresAt: 999, GrantID: "dg_test", IssuedAt: 100, Nonce: "nonce", Origin: "http://127.0.0.1:8080", PrincipalID: "u_test", Purpose: "auth.login", RootPublicKey: "root", Scopes: []string{"chat.read", "chat.write"}, ServerID: "srv_test", Version: 1}
	expected := `{"auth_epoch":1,"challenge_id":"ac_test","device_public_key":"device","expires_at":160,"grant_expires_at":999,"grant_id":"dg_test","issued_at":100,"nonce":"nonce","origin":"http://127.0.0.1:8080","principal_id":"u_test","purpose":"auth.login","root_public_key":"root","scopes":["chat.read","chat.write"],"server_id":"srv_test","v":1}`
	actual, err := transcript.Canonical()
	if err != nil || string(actual) != expected {
		t.Fatalf("%s %v", actual, err)
	}
	login, err := transcript.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(login, append([]byte("space/auth-login/v1\x00"), []byte(expected)...)) {
		t.Fatal("Префикс или байты подписи изменились")
	}
	transcript.Purpose = "device.register"
	registered, err := transcript.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(login, registered) {
		t.Fatal("Назначения подписи не разделены")
	}
	transcript.ServerID = "не ASCII"
	if _, err := transcript.Canonical(); err == nil {
		t.Fatal("Не поддержанный профиль принят")
	}
}
