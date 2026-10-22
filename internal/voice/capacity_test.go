package voice

import (
	"context"
	"fmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestConfiguredCapacityAndReduction(t *testing.T) {
	s := &Service{sessions: make(map[string]*mediaSession)}
	ctx := context.Background()
	for i := 0; i < 32; i++ {
		identity := Identity{Actor: Actor{"s", fmt.Sprint(i), "d"}, Capacity: 32}
		if _, err := s.reserve(ctx, identity, "room"); err != nil {
			t.Fatal(i, err)
		}
	}
	extra := Identity{Actor: Actor{"s", "extra", "d"}, Capacity: 32}
	if _, err := s.reserve(ctx, extra, "room"); status.Code(err) != codes.ResourceExhausted {
		t.Fatal(err)
	}
	extra.Capacity = 2
	if got := s.snapshot("room", extra); got.Capacity != 2 || len(got.Participants) != 32 {
		t.Fatal(got)
	}
	if _, err := s.reserve(ctx, extra, "room"); status.Code(err) != codes.ResourceExhausted {
		t.Fatal(err)
	}
	for id := range s.sessions {
		s.remove(id)
	}
	for i := 0; i < 2; i++ {
		extra.Actor.PrincipalID = fmt.Sprint(i)
		if _, err := s.reserve(ctx, extra, "room"); err != nil {
			t.Fatal(err)
		}
	}
	extra.Actor.PrincipalID = "extra"
	if _, err := s.reserve(ctx, extra, "room"); status.Code(err) != codes.ResourceExhausted {
		t.Fatal(err)
	}
}
