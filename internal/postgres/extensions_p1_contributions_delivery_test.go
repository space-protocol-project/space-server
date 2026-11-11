package postgres

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
)

func TestExtensionP1ContributionsDelivery(t *testing.T) {
	for _, mode := range deliveryTransportModes {
		for _, change := range []string{"acl", "logout", "registry"} {
			t.Run(mode+"/"+change, func(t *testing.T) { contributionDelivery(t, mode, change) })
		}
	}
}

func contributionDelivery(t *testing.T, mode, change string) {
	f := newChannelFixture(t)
	c := f.create(t, "contribution_wire")
	f.acl(t, c, roleRule("member", permissions(true, true, false, false)))
	member, _ := f.enroll(t, true)
	h := &extensionP1Probe{}
	s := contributionService(t, f, h, c.Id)
	managedChange(t, f, s, "enable", 1)
	ready, resume := make(chan struct{}), make(chan struct{})
	inner := s.deliveryMux()
	server := newDeliveryTestServer(t, mode, s.deliveryHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r)
		close(ready)
		select {
		case <-resume:
		case <-r.Context().Done():
		}
	})))
	defer server.Close()
	defer close(resume)
	client := rpc.NewExtensionP1ServiceClient(server.Client(), server.URL)
	done := make(chan error, 1)
	request := connect.NewRequest(&pb.ListExtensionContributionsRequest{InstanceId: "demo"})
	request.Header().Set("Authorization", outgoingAuthorization(member))
	go func() {
		result, err := client.ListExtensionContributions(f.ctx, request)
		if result != nil {
			done <- context.Canceled
			return
		}
		done <- err
	}()
	deliverySignal(t, ready)
	var err error
	switch change {
	case "acl":
		f.acl(t, c, roleRule("member", permissions(false, false, false, false)))
	case "logout":
		_, err = f.auth.Logout(member, &pb.LogoutRequest{})
	case "registry":
		_, err = f.store.pool.Exec(f.ctx, "UPDATE extension_p1_installations SET revision=revision+1 WHERE installation='install'")
	}
	if err != nil {
		t.Fatal(err)
	}
	resume <- struct{}{}
	failure := deliveryAwait(t, done)
	expected := map[string]connect.Code{"acl": connect.CodeNotFound, "logout": connect.CodeUnauthenticated, "registry": connect.CodeFailedPrecondition}[change]
	if connect.CodeOf(failure) != expected || h.calls.Load() != 0 {
		t.Fatal(failure, h.calls.Load())
	}
}
