package upgrade

import (
	"github.com/space-protocol-project/space-server/experiments/extensions/lifecycle"
	"testing"
)

func TestConcurrentBegin(t *testing.T) {
	p, b, v, c, _ := fixture()
	coordinator, _ := lifecycle.New("disabled", p.Revision)
	observation, err := Begin(p, b, v, c, true)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan string, 32)
	for range 32 {
		go func() { results <- coordinator.Apply(observation).Code }()
	}
	success := 0
	for range 32 {
		switch code := <-results; code {
		case "OK":
			success++
		case "Aborted":
		default:
			t.Fatal(code)
		}
	}
	if success != 1 {
		t.Fatal(success)
	}
}
