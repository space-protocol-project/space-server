package push

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAndroidSharedVector(t *testing.T) {
	raw, err := os.ReadFile("testdata/android-vector.properties")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			values[key] = value
		}
	}
	key, err := base64.RawURLEncoding.DecodeString(values["key"])
	if err != nil {
		t.Fatal(err)
	}
	target, err := Open(key, values["envelope"], time.Unix(1800000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := json.Marshal(target)
	if string(plain) != values["plain"] {
		t.Fatal("форматы runtime и Android расходятся")
	}
}
