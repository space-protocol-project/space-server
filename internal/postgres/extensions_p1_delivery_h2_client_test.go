package postgres

import (
	"bytes"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
	"google.golang.org/protobuf/encoding/protojson"
)

type deliveryH2Response struct {
	status       string
	body         []byte
	ended, reset bool
}

type deliveryH2Client struct {
	conn      *tls.Conn
	frames    *http2.Framer
	authority string
	responses map[uint32]*deliveryH2Response
}

func newDeliveryH2Client(t *testing.T, server *httptest.Server, window uint32) *deliveryH2Client {
	t.Helper()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	config := server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	config.NextProtos = []string{"h2"}
	conn, err := tls.Dial("tcp", u.Host, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if conn.ConnectionState().NegotiatedProtocol != "h2" {
		t.Fatal("HTTP/2 не согласован")
	}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err = conn.Write([]byte(http2.ClientPreface)); err != nil {
		t.Fatal(err)
	}
	frames := http2.NewFramer(conn, conn)
	frames.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
	if err = frames.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: window}); err != nil {
		t.Fatal(err)
	}
	return &deliveryH2Client{conn, frames, u.Host, make(map[uint32]*deliveryH2Response)}
}

func (c *deliveryH2Client) query(t *testing.T, id uint32, authorization string) {
	t.Helper()
	var headers bytes.Buffer
	encoder := hpack.NewEncoder(&headers)
	for _, field := range []hpack.HeaderField{
		{Name: ":method", Value: "POST"}, {Name: ":scheme", Value: "https"},
		{Name: ":authority", Value: c.authority}, {Name: ":path", Value: extensionDeliveryPath + "QueryExtension"},
		{Name: "content-type", Value: "application/json"}, {Name: "connect-protocol-version", Value: "1"},
		{Name: "authorization", Value: authorization},
	} {
		if err := encoder.WriteField(field); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.frames.WriteHeaders(http2.HeadersFrameParam{StreamID: id, BlockFragment: headers.Bytes(), EndHeaders: true}); err != nil {
		t.Fatal(err)
	}
	body, err := protojson.Marshal(extensionP1Query())
	if err != nil {
		t.Fatal(err)
	}
	if err = c.frames.WriteData(id, true, body); err != nil {
		t.Fatal(err)
	}
}

func (c *deliveryH2Client) window(t *testing.T, id, amount uint32) {
	t.Helper()
	if err := c.frames.WriteWindowUpdate(id, amount); err != nil {
		t.Fatal(err)
	}
}

func (c *deliveryH2Client) response(id uint32) *deliveryH2Response {
	if c.responses[id] == nil {
		c.responses[id] = new(deliveryH2Response)
	}
	return c.responses[id]
}

func (c *deliveryH2Client) until(t *testing.T, ready func() bool) {
	t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for !ready() {
		frame, err := c.frames.ReadFrame()
		if err != nil {
			t.Fatal("Чтение HTTP/2", err)
		}
		c.accept(t, frame)
	}
}

func (c *deliveryH2Client) accept(t *testing.T, frame http2.Frame) {
	t.Helper()
	switch f := frame.(type) {
	case *http2.SettingsFrame:
		if !f.IsAck() {
			if err := c.frames.WriteSettingsAck(); err != nil {
				t.Fatal(err)
			}
		}
	case *http2.MetaHeadersFrame:
		r := c.response(f.StreamID)
		for _, field := range f.Fields {
			if field.Name == ":status" {
				r.status = field.Value
			}
		}
		r.ended = f.StreamEnded()
	case *http2.DataFrame:
		r := c.response(f.StreamID)
		r.body = append(r.body, f.Data()...)
		r.ended = f.StreamEnded()
	case *http2.RSTStreamFrame:
		c.response(f.StreamID).reset = true
	case *http2.GoAwayFrame:
		t.Fatalf("Соединение закрыто вместо отдельного потока: %v", f.ErrCode)
	}
}
