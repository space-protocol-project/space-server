package extensionp1

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMutationPlanRejectsUntrustedOperations(t *testing.T) {
	valid := `{"expectedStateRevision":"0","operations":[{"key":"note","putJson":"e30="}],"events":[{"type":"note_changed","payloadJson":"e30="}]}`
	wrap := func(plan string) []byte {
		return []byte(`{"ui_api":"space.ui.p1","document":{"ui_api":"space.ui.p1"},"plan":` + plan + `}`)
	}
	if _, _, err := ParseMutation(wrap(valid), 0, map[string]bool{"note_changed": true}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{
		wrap(strings.Replace(valid, `"expectedStateRevision":"0"`, `"expectedStateRevision":"1"`, 1)),
		wrap(strings.Replace(valid, `"key":"note"`, `"key":"bad\u0000key"`, 1)),
		wrap(strings.Replace(valid, `"putJson":"e30="`, `"delete":false`, 1)),
		wrap(strings.Replace(valid, `"putJson":"e30="`, `"putJson":"/w=="`, 1)),
		wrap(strings.Replace(valid, `"putJson":"e30="`, `"putJson":"e30=","delete":true`, 1)),
		wrap(strings.Replace(valid, `"key":"note"`, `"key":"note","sql":"DELETE FROM users"`, 1)),
		wrap(strings.Replace(valid, `"note_changed"`, `"unknown_event"`, 1)),
		wrap(strings.Replace(valid, `"operations":[`, `"operations":[{"key":"note","delete":true},`, 1)),
		wrap(strings.Replace(valid, `"key":"note"`, `"key":"note","key":"other"`, 1)),
		append(wrap(valid), []byte(strings.Repeat(" ", 16384))...),
	} {
		if _, _, err := ParseMutation(raw, 0, map[string]bool{"note_changed": true}); err == nil {
			t.Fatal("Принят недопустимый план", json.Valid(raw))
		}
	}
}
