package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	pb "github.com/space-protocol-project/space-protocol/server/gen/space/v1"
	"github.com/space-protocol-project/space-protocol/server/internal/authn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type client struct {
	origin, serverID, token string
	http                    *http.Client
}
type apiError struct {
	status int
	path   string
}

func (e apiError) Error() string { return fmt.Sprintf("%s: HTTP %d", e.path, e.status) }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	origin := flag.String("origin", "http://127.0.0.1:8080", "Локальный origin")
	flag.Parse()
	parsed, err := url.Parse(*origin)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || net.ParseIP(parsed.Hostname()) == nil || !net.ParseIP(parsed.Hostname()).IsLoopback() {
		return fmt.Errorf("Нужен локальный HTTP origin без пути")
	}
	c := &client{origin: *origin, http: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("Redirect запрещён") }}}
	response, err := c.http.Get(c.origin + "/.well-known/space-protocol")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var discovery struct {
		ServerID string `json:"server_id"`
	}
	if response.StatusCode != 200 {
		return fmt.Errorf("Discovery: HTTP %d", response.StatusCode)
	}
	if err = json.NewDecoder(response.Body).Decode(&discovery); err != nil {
		return err
	}
	if discovery.ServerID == "" {
		return fmt.Errorf("Нет server_id")
	}
	c.serverID = discovery.ServerID
	root, rootPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	device, devicePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	registered, err := c.authorize("device.register", "", root, device, rootPrivate)
	if err != nil {
		return err
	}
	session, err := c.authorize("auth.login", registered.GrantId, root, device, devicePrivate)
	if err != nil {
		return err
	}
	c.token = session.AccessToken
	created := new(pb.CreateContentResponse)
	key := make([]byte, 16)
	if _, err = rand.Read(key); err != nil {
		return err
	}
	request := &pb.CreateContentRequest{ChannelId: "general", Text: "Сообщение после входа по ключу устройства", IdempotencyKey: base64.RawURLEncoding.EncodeToString(key)}
	if err = c.call("POST", "/api/v1/channels/general/content", request, created); err != nil {
		return err
	}
	repeated := new(pb.CreateContentResponse)
	if err = c.call("POST", "/api/v1/channels/general/content", request, repeated); err != nil {
		return err
	}
	if repeated.GetContent().GetId() != created.GetContent().GetId() {
		return fmt.Errorf("Повтор создал другое сообщение")
	}
	events := new(pb.ListEventsResponse)
	if err = c.call("GET", "/api/v1/channels/general/events", nil, events); err != nil {
		return err
	}
	fmt.Printf("Вход выполнен; principal=%s; сообщение=%s; страница событий=%d\n", registered.PrincipalId, created.Content.Id, len(events.Events))
	if _, err = c.authorize("device.revoke", registered.GrantId, root, device, rootPrivate); err != nil {
		return err
	}
	err = c.call("GET", "/api/v1/channels/general/content", nil, new(pb.ListContentResponse))
	var failure apiError
	if !errors.As(err, &failure) || failure.status != 401 {
		return fmt.Errorf("После отзыва ожидался HTTP 401: %v", err)
	}
	fmt.Println("Устройство отозвано. Тестовые приватные ключи существовали только в памяти.")
	return nil
}

func (c *client) call(method, path string, input, output proto.Message) error {
	var body []byte
	var err error
	if input != nil {
		body, err = protojson.Marshal(input)
		if err != nil {
			return err
		}
	}
	request, err := http.NewRequest(method, c.origin+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	if err != nil {
		return err
	}
	if response.StatusCode != 200 {
		return apiError{status: response.StatusCode, path: path}
	}
	return protojson.Unmarshal(data, output)
}

func (c *client) authorize(purpose, grant string, root, device ed25519.PublicKey, private ed25519.PrivateKey) (*pb.CompleteChallengeResponse, error) {
	request := &pb.CreateChallengeRequest{Purpose: purpose, GrantId: grant}
	if purpose == "device.register" {
		request.RootPublicKey = root
		request.DevicePublicKey = device
	}
	if purpose == "device.revoke" {
		request.RootPublicKey = root
	}
	challenge := new(pb.CreateChallengeResponse)
	if err := c.call("POST", "/api/v1/auth/challenges", request, challenge); err != nil {
		return nil, err
	}
	var transcript authn.Transcript
	if err := json.Unmarshal(challenge.Transcript, &transcript); err != nil {
		return nil, err
	}
	if err := c.validate(transcript, challenge, root, device, purpose, grant); err != nil {
		return nil, err
	}
	signing, err := transcript.SigningBytes()
	if err != nil {
		return nil, err
	}
	result := new(pb.CompleteChallengeResponse)
	err = c.call("POST", "/api/v1/auth/sessions", &pb.CompleteChallengeRequest{ChallengeId: challenge.ChallengeId, Signature: ed25519.Sign(private, signing)}, result)
	return result, err
}

func (c *client) validate(t authn.Transcript, response *pb.CreateChallengeResponse, root, device ed25519.PublicKey, purpose, grant string) error {
	canonical, err := t.Canonical()
	if err != nil {
		return err
	}
	valid := bytes.Equal(canonical, response.Transcript) && t.Version == 1 && t.AuthEpoch == 1 && t.ServerID == c.serverID && t.Origin == c.origin && t.Purpose == purpose && t.ChallengeID == response.ChallengeId && t.RootPublicKey == base64.RawURLEncoding.EncodeToString(root) && t.DevicePublicKey == base64.RawURLEncoding.EncodeToString(device) && t.PrincipalID == authn.PrincipalID(root)
	nonce, err := base64.RawURLEncoding.DecodeString(t.Nonce)
	valid = valid && err == nil && len(nonce) == 32 && strings.HasPrefix(t.GrantID, "dg_") && len(t.GrantID) == 46 && strings.HasPrefix(t.ChallengeID, "ac_") && len(t.ChallengeID) == 46
	valid = valid && len(t.Scopes) == 2 && t.Scopes[0] == "chat.read" && t.Scopes[1] == "chat.write" && t.ExpiresAt == t.IssuedAt+60 && t.ExpiresAt > time.Now().Unix() && t.IssuedAt <= time.Now().Unix()+5 && t.GrantExpiresAt > t.ExpiresAt
	if purpose == "device.register" {
		valid = valid && t.GrantExpiresAt == t.IssuedAt+30*24*60*60
	} else {
		valid = valid && t.GrantID == grant
	}
	if !valid {
		return fmt.Errorf("Transcript не соответствует ожидаемому локальному серверу, ключам или операции")
	}
	return nil
}
