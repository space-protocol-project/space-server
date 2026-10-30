package extensionp1

import (
	"encoding/json"
	"os"
	"testing"
)

func TestRetryContract(t *testing.T) {
	data, err := os.ReadFile("testdata/retry.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Version string `json:"version"`
		Cases   []struct {
			Name     string           `json:"name"`
			Input    RetryObservation `json:"input"`
			Expected RetryDecision    `json:"expected"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != "space.retry.p1" || len(fixture.Cases) != 40 {
		t.Fatal("Unexpected contract fixture")
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if got := DecideRetry(c.Input); got != c.Expected {
				t.Fatalf("got=%+v want=%+v", got, c.Expected)
			}
		})
	}
}

func TestRetryUnknownSurvivesDenied(t *testing.T) {
	o := RetryObservation{Code: "TransportLost", Attempt: 1, RemainingMS: 30000, WindowKnown: true, SameRequest: true}
	first := DecideRetry(o)
	if !first.Unknown || first.Action != "retry_same_request" {
		t.Fatal(first)
	}
	o.PreviousUnknown = first.Unknown
	o.Attempt = 2
	o.Code = "PermissionDenied"
	denied := DecideRetry(o)
	if !denied.Unknown || denied.Action != "stop" {
		t.Fatal(denied)
	}
	// Только проверенный успешный ответ разрешает неопределённость.
	o.Code = "OK"
	if got := DecideRetry(o); got.Unknown || got.Action != "confirmed" {
		t.Fatal(got)
	}
}
