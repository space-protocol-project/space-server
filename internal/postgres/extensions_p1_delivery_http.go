package postgres

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"github.com/space-protocol-project/space-server/internal/authn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const extensionDeliveryPath = "/space.v1.ExtensionP1Service/"
const extensionDeliveryLimit = 128 * 1024

type extensionWireKey struct{}
type extensionWireCapture struct{ snapshot *extensionDeliverySnapshot }

type extensionWireBuffer struct {
	header http.Header
	body   bytes.Buffer
	status int
	err    error
}

func (b *extensionWireBuffer) Header() http.Header { return b.header }
func (b *extensionWireBuffer) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}
func (b *extensionWireBuffer) Write(body []byte) (int, error) {
	b.WriteHeader(http.StatusOK)
	if b.err != nil {
		return 0, b.err
	}
	if len(body) > extensionDeliveryLimit-b.body.Len() {
		b.err = errors.New("delivery_response_limit")
		return 0, b.err
	}
	return b.body.Write(body)
}

// NewDeliveryHandler — отдельный прямой Connect unary путь. Он не использует
// gRPC-мост, не включается автоматически и не поддерживает gRPC/streaming.
func (s *ExtensionP1ManagedService) NewDeliveryHandler() (string, http.Handler) {
	return extensionDeliveryPath, s.deliveryHTTP(s.deliveryMux())
}
func (s *ExtensionP1ManagedService) deliveryMux() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(extensionDeliveryPath+"ListExtensionContributions", deliveryUnary(extensionDeliveryPath+"ListExtensionContributions", s.ListExtensionContributions))
	mux.Handle(extensionDeliveryPath+"QueryExtension", deliveryUnary(extensionDeliveryPath+"QueryExtension", s.QueryExtension))
	mux.Handle(extensionDeliveryPath+"ExecuteExtensionCommand", deliveryUnary(extensionDeliveryPath+"ExecuteExtensionCommand", s.ExecuteExtensionCommand))
	mux.Handle(extensionDeliveryPath+"GetExtensionMutationContext", deliveryUnary(extensionDeliveryPath+"GetExtensionMutationContext", s.GetExtensionMutationContext))
	mux.Handle(extensionDeliveryPath+"ReconcileExtensionCommand", deliveryUnary(extensionDeliveryPath+"ReconcileExtensionCommand", s.ReconcileExtensionCommand))
	mux.Handle(extensionDeliveryPath+"ListExtensionInstallations", deliveryUnary(extensionDeliveryPath+"ListExtensionInstallations", s.ListExtensionInstallations))
	mux.Handle(extensionDeliveryPath+"GetExtensionInstallation", deliveryUnary(extensionDeliveryPath+"GetExtensionInstallation", s.GetExtensionInstallation))
	mux.Handle(extensionDeliveryPath+"ChangeExtensionInstallation", deliveryUnary(extensionDeliveryPath+"ChangeExtensionInstallation", s.ChangeExtensionInstallation))
	mux.Handle(extensionDeliveryPath+"ListExtensionInstallationAudit", deliveryUnary(extensionDeliveryPath+"ListExtensionInstallationAudit", s.ListExtensionInstallationAudit))
	return mux
}
func deliveryUnary[Q, R any](path string, call func(context.Context, *Q) (*R, error)) http.Handler {
	return connect.NewUnaryHandler(path, func(ctx context.Context, r *connect.Request[Q]) (*connect.Response[R], error) {
		out, err := call(ctx, r.Msg)
		if err != nil {
			return nil, connect.NewError(connect.Code(status.Code(err)), errors.New(status.Convert(err).Message()))
		}
		return connect.NewResponse(out), nil
	}, connect.WithReadMaxBytes(20*1024))
}
func (s *ExtensionP1ManagedService) deliveryHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || (kind != "application/proto" && kind != "application/json") {
			http.Error(w, "Connect unary required", http.StatusUnsupportedMediaType)
			return
		}
		if len(r.Header.Values("Authorization")) != 1 {
			writeDeliveryError(w, r, mutationError(codes.Unauthenticated, "session_required"))
			return
		}
		ctx, err := authn.AuthorizeCall(metadata.NewIncomingContext(r.Context(), metadata.Pairs("authorization", r.Header.Get("Authorization"))), s.control.store, r.URL.Path)
		if err != nil {
			writeDeliveryError(w, r, err)
			return
		}
		if !bufferedDeliveryPath(r.URL.Path) {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		capture := &extensionWireCapture{}
		ctx = context.WithValue(ctx, extensionWireKey{}, capture)
		buffer := &extensionWireBuffer{header: make(http.Header)}
		next.ServeHTTP(buffer, r.WithContext(ctx))
		s.sendHTTPDelivery(ctx, capture.snapshot, buffer, w, r)
	})
}

func (s *ExtensionP1ManagedService) sendHTTPDelivery(ctx context.Context, snapshot *extensionDeliverySnapshot, buffer *extensionWireBuffer, w http.ResponseWriter, r *http.Request) {
	if buffer.err != nil {
		code := codes.ResourceExhausted
		if r.URL.Path == extensionDeliveryPath+"ExecuteExtensionCommand" {
			code = codes.Unavailable
		}
		writeDeliveryError(w, r, mutationError(code, "delivery_response_limit"))
		return
	}
	if buffer.status != http.StatusOK {
		copyDeliveryHeaders(w, buffer)
		w.WriteHeader(buffer.status)
		_, _ = w.Write(buffer.body.Bytes())
		return
	}
	started, err := s.flushDelivery(ctx, snapshot, buffer, w)
	if err != nil {
		if started {
			panic(http.ErrAbortHandler)
		}
		if r.URL.Path == extensionDeliveryPath+"ExecuteExtensionCommand" {
			err = mutationError(codes.Unavailable, "delivery_not_confirmed")
		}
		writeDeliveryError(w, r, err)
	}
}

func bufferedDeliveryPath(path string) bool {
	switch path {
	case extensionDeliveryPath + "ListExtensionContributions", extensionDeliveryPath + "QueryExtension", extensionDeliveryPath + "ExecuteExtensionCommand", extensionDeliveryPath + "GetExtensionMutationContext":
		return true
	default:
		return false
	}
}

func writeDeliveryError(w http.ResponseWriter, r *http.Request, err error) {
	_ = connect.NewErrorWriter().Write(w, r, connect.NewError(connect.Code(status.Code(err)), errors.New(status.Convert(err).Message())))
}
func copyDeliveryHeaders(w http.ResponseWriter, b *extensionWireBuffer) {
	for key, values := range b.header {
		w.Header()[key] = append([]string(nil), values...)
	}
	w.Header().Set("Content-Length", strconv.Itoa(b.body.Len()))
}

func (s *ExtensionP1ManagedService) flushDelivery(ctx context.Context, before *extensionDeliverySnapshot, b *extensionWireBuffer, w http.ResponseWriter) (bool, error) {
	if before == nil {
		return false, mutationError(codes.PermissionDenied, "delivery_snapshot_required")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := s.control.store.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	after, err := s.deliverySnapshotTx(ctx, tx, before.principal.Instance, before.write)
	if err != nil {
		return false, err
	}
	if err = compareDeliverySnapshot(*before, after); err != nil {
		return false, err
	}
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	if err = controller.SetWriteDeadline(deadline); err != nil {
		return false, err
	}
	defer controller.SetWriteDeadline(time.Time{})
	if err = ctx.Err(); err != nil {
		return false, err
	}
	copyDeliveryHeaders(w, b)
	w.WriteHeader(http.StatusOK)
	n, err := w.Write(b.body.Bytes())
	if err != nil {
		return true, err
	}
	if n != b.body.Len() {
		return true, io.ErrShortWrite
	}
	return true, controller.Flush()
}
