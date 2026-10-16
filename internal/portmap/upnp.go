package portmap

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type upnpDevice struct {
	Services []upnpService `xml:"serviceList>service"`
	Devices  []upnpDevice  `xml:"deviceList>device"`
}
type upnpService struct {
	Type    string `xml:"serviceType"`
	Control string `xml:"controlURL"`
}
type upnpClient struct {
	route              Route
	service            upnpService
	client             *http.Client
	internal, external uint16
	marker             string
}

func gatewayURL(raw string, route Route) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("UPnP: недопустимый URL")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || ip.Unmap() != route.Gateway.Addr().Unmap() {
		return nil, fmt.Errorf("UPnP: URL указывает не на шлюз")
	}
	return u, nil
}

func upnpHTTP(route Route) *http.Client {
	dialer := &net.Dialer{Timeout: 2 * time.Second, LocalAddr: &net.TCPAddr{IP: net.IP(route.Local.AsSlice())}}
	return &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("UPnP: перенаправления запрещены")
	},
		Transport: &http.Transport{Proxy: nil, DialContext: dialer.DialContext, DisableKeepAlives: true, MaxResponseHeaderBytes: 8192}}
}

func discoverUPnP(ctx context.Context, route Route) (*url.URL, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IP(route.Local.AsSlice())})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline := time.Now().Add(time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	_ = conn.SetDeadline(deadline)
	packet := []byte("M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 1\r\nST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\n\r\n")
	for _, target := range []string{net.JoinHostPort(route.Gateway.Addr().String(), "1900"), "239.255.255.250:1900"} {
		destination, _ := net.ResolveUDPAddr("udp4", target)
		_, _ = conn.WriteToUDP(packet, destination)
	}
	for i := 0; i < 12; i++ {
		packet := make([]byte, 2049)
		n, source, err := conn.ReadFromUDP(packet)
		if err != nil {
			return nil, err
		}
		ip, ok := netip.AddrFromSlice(source.IP)
		if !ok || ip.Unmap() != route.Gateway.Addr().Unmap() || n > 2048 {
			continue
		}
		response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(packet[:n])), nil)
		if err != nil {
			continue
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			continue
		}
		if location, err := gatewayURL(response.Header.Get("Location"), route); err == nil {
			return location, nil
		}
	}
	return nil, fmt.Errorf("UPnP: шлюз не ответил")
}

func readUPnP(ctx context.Context, client *http.Client, request *http.Request) ([]byte, int, error) {
	response, err := client.Do(request.WithContext(ctx))
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(content) > 65536 {
		return nil, 0, fmt.Errorf("UPnP: ответ превышает бюджет")
	}
	return content, response.StatusCode, nil
}

func findService(device upnpDevice, depth int) (upnpService, bool) {
	if depth > 8 {
		return upnpService{}, false
	}
	for _, service := range device.Services {
		switch service.Type {
		case "urn:schemas-upnp-org:service:WANIPConnection:1", "urn:schemas-upnp-org:service:WANIPConnection:2", "urn:schemas-upnp-org:service:WANPPPConnection:1":
			return service, true
		}
	}
	for _, child := range device.Devices {
		if service, ok := findService(child, depth+1); ok {
			return service, true
		}
	}
	return upnpService{}, false
}

func createUPnP(ctx context.Context, route Route, internal, external uint16) (*Mapping, error) {
	location, err := discoverUPnP(ctx, route)
	if err != nil {
		return nil, err
	}
	client := upnpHTTP(route)
	request, _ := http.NewRequest(http.MethodGet, location.String(), nil)
	content, status, err := readUPnP(ctx, client, request)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("UPnP: нет описания IGD")
	}
	// Проверяем бюджет глубины до Unmarshal вложенного описания.
	if _, err := soapFields(content); err != nil {
		return nil, err
	}
	var description struct {
		Device upnpDevice `xml:"device"`
	}
	if err = xml.Unmarshal(content, &description); err != nil {
		return nil, err
	}
	service, ok := findService(description.Device, 0)
	if !ok {
		return nil, fmt.Errorf("UPnP: нет WANIPConnection/WANPPPConnection")
	}
	control, err := url.Parse(service.Control)
	if err != nil {
		return nil, err
	}
	control, err = gatewayURL(location.ResolveReference(control).String(), route)
	if err != nil {
		return nil, err
	}
	service.Control = control.String()
	var nonce [12]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	u := &upnpClient{route: route, service: service, client: client, internal: internal, external: external, marker: "Space-" + hex.EncodeToString(nonce[:])}
	return u.mapPort(ctx, false)
}

func soapFields(content []byte) (map[string]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(content))
	fields := map[string]string{}
	stack := []string{}
	count := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return fields, nil
		}
		if err != nil {
			return nil, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			count++
			if len(stack) > 24 || count > 1024 {
				return nil, fmt.Errorf("UPnP: слишком сложный XML")
			}
			stack = append(stack, token.Name.Local)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				key := stack[len(stack)-1]
				fields[key] += string(token)
				if len(fields[key]) > 8192 {
					return nil, fmt.Errorf("UPnP: слишком длинное поле")
				}
			}
		}
	}
}

func (u *upnpClient) call(ctx context.Context, action string, values [][2]string) (map[string]string, error) {
	var body bytes.Buffer
	fmt.Fprintf(&body, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:%s xmlns:u="%s">`, action, u.service.Type)
	for _, value := range values {
		fmt.Fprintf(&body, "<%s>", value[0])
		_ = xml.EscapeText(&body, []byte(value[1]))
		fmt.Fprintf(&body, "</%s>", value[0])
	}
	fmt.Fprintf(&body, "</u:%s></s:Body></s:Envelope>", action)
	request, _ := http.NewRequest(http.MethodPost, u.service.Control, &body)
	request.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	request.Header.Set("SOAPAction", `"`+u.service.Type+"#"+action+`"`)
	content, status, err := readUPnP(ctx, u.client, request)
	if err != nil {
		return nil, err
	}
	fields, err := soapFields(content)
	if err != nil {
		return nil, err
	}
	if status != 200 && strings.TrimSpace(fields["errorCode"]) == "" {
		return nil, fmt.Errorf("UPnP: неверный статус SOAP")
	}
	return fields, nil
}

func (u *upnpClient) key() [][2]string {
	return [][2]string{{"NewRemoteHost", ""}, {"NewExternalPort", strconv.Itoa(int(u.external))}, {"NewProtocol", "TCP"}}
}
func (u *upnpClient) own(fields map[string]string) bool {
	return strings.TrimSpace(fields["NewInternalClient"]) == u.route.Local.String() && strings.TrimSpace(fields["NewInternalPort"]) == strconv.Itoa(int(u.internal)) && fields["NewPortMappingDescription"] == u.marker
}
func (u *upnpClient) mapPort(ctx context.Context, renewing bool) (*Mapping, error) {
	address, err := u.call(ctx, "GetExternalIPAddress", nil)
	if err != nil {
		return nil, err
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(address["NewExternalIPAddress"]))
	if err != nil || !PublicIP(ip) || !ip.Is4() {
		return nil, fmt.Errorf("UPnP: шлюз не имеет публичного IPv4")
	}
	existing, err := u.call(ctx, "GetSpecificPortMappingEntry", u.key())
	if err != nil {
		return nil, err
	}
	code := strings.TrimSpace(existing["errorCode"])
	if code != "714" && !(code == "" && renewing && u.own(existing)) {
		return nil, fmt.Errorf("UPnP: порт занят или владение правилом не подтверждено")
	}
	values := append(u.key(), [][2]string{{"NewInternalPort", strconv.Itoa(int(u.internal))}, {"NewInternalClient", u.route.Local.String()}, {"NewEnabled", "1"}, {"NewPortMappingDescription", u.marker}, {"NewLeaseDuration", strconv.Itoa(Lifetime)}}...)
	fields, err := u.call(ctx, "AddPortMapping", values)
	if err != nil {
		return nil, err
	}
	if fields["errorCode"] != "" {
		return nil, fmt.Errorf("UPnP: временная аренда недоступна")
	}
	confirmed, err := u.call(ctx, "GetSpecificPortMappingEntry", u.key())
	if err != nil {
		_ = u.remove(ctx)
		return nil, err
	}
	seconds, err := strconv.ParseUint(strings.TrimSpace(confirmed["NewLeaseDuration"]), 10, 32)
	if err != nil || !u.own(confirmed) || !validLease(uint32(seconds)) {
		if u.own(confirmed) {
			_ = u.remove(ctx)
		}
		return nil, fmt.Errorf("UPnP: временная аренда не подтверждена")
	}
	return &Mapping{External: netip.AddrPortFrom(ip, u.external), Protocol: "UPnP", Lifetime: time.Duration(seconds) * time.Second,
		Renew: func(ctx context.Context) (*Mapping, error) { return u.mapPort(ctx, true) }, Delete: u.remove}, nil
}
func (u *upnpClient) remove(ctx context.Context) error {
	fields, err := u.call(ctx, "GetSpecificPortMappingEntry", u.key())
	if err != nil {
		return err
	}
	if fields["errorCode"] == "714" {
		return nil
	}
	if !u.own(fields) {
		return fmt.Errorf("UPnP: чужое правило не удалено")
	}
	result, err := u.call(ctx, "DeletePortMapping", u.key())
	if err != nil {
		return err
	}
	if result["errorCode"] != "" && result["errorCode"] != "714" {
		return fmt.Errorf("UPnP: удаление не подтверждено")
	}
	return nil
}
