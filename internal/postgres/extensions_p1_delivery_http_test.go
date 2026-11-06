package postgres

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	rpc "github.com/space-protocol-project/space-server/gen/space/v1/spacev1connect"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

func deliverySignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("Нет сигнала барьера")
	}
}

func TestExtensionP1DeliveryAdmissionCommit(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "admission_commit")
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	member, _ := f.enroll(t, true)
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, h, mutationBindings(c.Id, []byte{1}))
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	ctx, err := authn.AuthorizeCall(metadata.NewIncomingContext(f.ctx, metadata.Pairs("authorization", "Bearer "+lifecycleToken(member))), f.store, "/space.p1.delivery/Command")
	if err != nil {
		t.Fatal(err)
	}
	call, release, err := s.begin(ctx, "demo", true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	f.acl(t, c, roleRule("member", permissions(false, false, false, false)))
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	info, err := s.service.GetExtensionMutationContext(call, &pb.GetExtensionMutationContextRequest{InstanceId: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	r := mutationRequest("admission-commit-001", 0)
	r.HistoryId = info.HistoryId
	_, err = s.service.ExecuteExtensionCommand(call, r)
	requireCode(t, err, codes.PermissionDenied)
	mutationCounts(t, f, 0, 0, 0, 0)
	if h.calls.Load() != 1 {
		t.Fatal("Снимок backend не проверен относительно допуска", h.calls.Load())
	}
}

func TestExtensionP1DeliveryHTTPRevokeFirst(t *testing.T) {
	for _, kind := range []string{"acl", "logout", "registry"} {
		t.Run(kind, func(t *testing.T) {
			f := newChannelFixture(t)
			c := f.create(t, "wire_before")
			f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
			member, _ := f.enroll(t, true)
			h := new(extensionP1Probe)
			s, err := f.store.NewExtensionP1ManagedService(f.ctx, h, mutationBindings(c.Id, []byte{1}))
			if err != nil {
				t.Fatal(err)
			}
			managedChange(t, f, s, "enable", 1)
			ready, resume := make(chan struct{}), make(chan struct{})
			inner := s.deliveryMux()
			server := httptest.NewServer(s.deliveryHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			request := connect.NewRequest(extensionP1Query())
			request.Header().Set("Authorization", outgoingAuthorization(member))
			go func() {
				result, err := client.QueryExtension(f.ctx, request)
				if result != nil {
					done <- context.Canceled
					return
				}
				done <- err
			}()
			deliverySignal(t, ready)
			switch kind {
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
			code := connect.CodeOf(failure)
			expected := map[string]connect.Code{"acl": connect.CodeNotFound, "logout": connect.CodeUnauthenticated, "registry": connect.CodeFailedPrecondition}[kind]
			if code != expected {
				t.Fatal(failure)
			}
			if h.calls.Load() != 1 {
				t.Fatal(h.calls.Load())
			}
		})
	}
}

func TestExtensionP1DeliveryHTTPCommittedRevoke(t *testing.T) {
	f := newChannelFixture(t)
	h := &mutationProbe{run: func(context.Context) ([]byte, error) { return mutationOutput(0), nil }}
	s, api := managedFixture(t, f, h)
	managedChange(t, f, s, "enable", 1)
	info, err := api.GetExtensionMutationContext(f.owner, &pb.GetExtensionMutationContextRequest{InstanceId: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	ready, resume := make(chan struct{}), make(chan struct{})
	inner := s.deliveryMux()
	server := httptest.NewServer(s.deliveryHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r)
		if r.URL.Path == extensionDeliveryPath+"ExecuteExtensionCommand" {
			close(ready)
			select {
			case <-resume:
			case <-r.Context().Done():
			}
		}
	})))
	defer server.Close()
	defer close(resume)
	client := rpc.NewExtensionP1ServiceClient(server.Client(), server.URL)
	r := mutationRequest("wire-committed-001", 0)
	r.HistoryId = info.HistoryId
	request := connect.NewRequest(r)
	request.Header().Set("Authorization", outgoingAuthorization(f.owner))
	done := make(chan error, 1)
	go func() {
		result, err := client.ExecuteExtensionCommand(f.ctx, request)
		if result != nil {
			done <- context.Canceled
			return
		}
		done <- err
	}()
	deliverySignal(t, ready)
	mutationCounts(t, f, 1, 1, 1, 1)
	if _, err := f.store.pool.Exec(f.ctx, "UPDATE extension_p1_installations SET revision=revision+1 WHERE installation='install'"); err != nil {
		t.Fatal(err)
	}
	resume <- struct{}{}
	if err := deliveryAwait(t, done); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatal(err)
	}
	reconcile := connect.NewRequest(&pb.ReconcileExtensionCommandRequest{Command: r})
	reconcile.Header().Set("Authorization", outgoingAuthorization(f.owner))
	receipt, err := client.ReconcileExtensionCommand(f.ctx, reconcile)
	if err != nil || receipt.Msg.Outcome != pb.ExtensionCommandOutcome_EXTENSION_COMMAND_OUTCOME_CONFIRMED {
		t.Fatal(receipt, err)
	}
	verifyExtensionCrashReceipt(t, f, receipt.Msg.Receipt)
	if h.calls.Load() != 1 {
		t.Fatal("Повторное исполнение", h.calls.Load())
	}
	mutationCounts(t, f, 1, 1, 1, 1)
}

type deliveryBlockingWriter struct {
	http.ResponseWriter
	sent   chan struct{}
	resume <-chan struct{}
}

func (w *deliveryBlockingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *deliveryBlockingWriter) Write(body []byte) (int, error) {
	n, err := w.ResponseWriter.Write(body)
	if err != nil {
		return n, err
	}
	if err := http.NewResponseController(w.ResponseWriter).Flush(); err != nil {
		return n, err
	}
	close(w.sent)
	<-w.resume
	return n, nil
}

func TestExtensionP1DeliveryHTTPWriteFirst(t *testing.T) {
	f := newChannelFixture(t)
	c := f.create(t, "wire_after")
	f.acl(t, c, roleRule("member", permissions(true, true, true, false)))
	member, _ := f.enroll(t, true)
	h := new(extensionP1Probe)
	s, err := f.store.NewExtensionP1ManagedService(f.ctx, h, mutationBindings(c.Id, []byte{1}))
	if err != nil {
		t.Fatal(err)
	}
	managedChange(t, f, s, "enable", 1)
	_, handler := s.NewDeliveryHandler()
	sent, resume := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(&deliveryBlockingWriter{w, sent, resume}, r)
	}))
	defer server.Close()
	defer close(resume)
	client := rpc.NewExtensionP1ServiceClient(server.Client(), server.URL)
	request := connect.NewRequest(extensionP1Query())
	request.Header().Set("Authorization", outgoingAuthorization(member))
	done := make(chan error, 1)
	go func() {
		result, err := client.QueryExtension(f.ctx, request)
		if err == nil && string(result.Msg.DocumentJson) != `{"ui_api":"space.ui.p1"}` {
			err = context.Canceled
		}
		done <- err
	}()
	deliverySignal(t, sent)
	if err := deliveryAwait(t, done); err != nil {
		t.Fatal("Клиент не прочитал настоящий ответ", err)
	}
	changed := make(chan error, 1)
	go func() {
		_, err := f.channels.UpdateChannelAccess(f.owner, &pb.UpdateChannelAccessRequest{ChannelId: c.Id, ExpectedRevision: c.Revision, Rules: []*pb.ChannelAccessRule{roleRule("member", permissions(false, false, false, false))}})
		changed <- err
	}()
	waitExtensionSQLLock(t, f, "FROM channels WHERE id")
	resume <- struct{}{}
	if err := deliveryAwait(t, changed); err != nil {
		t.Fatal(err)
	}
}

func deliveryAwait(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Нет результата операции")
		return nil
	}
}
