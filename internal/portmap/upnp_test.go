package portmap

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestUPnPURLConfinedToGateway(t *testing.T) {
	route := Route{Gateway: netip.MustParseAddrPort("192.168.1.1:5351"), Local: netip.MustParseAddr("192.168.1.2")}
	for _, url := range []string{"http://example.com/control", "http://127.0.0.1/", "http://192.168.1.2/", "https://192.168.1.1/", "http://user@192.168.1.1/", "http://192.168.1.1/#fragment"} {
		if _, err := gatewayURL(url, route); err == nil {
			t.Fatal(url)
		}
	}
	if _, err := gatewayURL("http://192.168.1.1:5000/control", route); err != nil {
		t.Fatal(err)
	}
}

func TestUPnPLeaseOwnership(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprint(foreign), func(t *testing.T) {
			exists := foreign
			owner := "Space-test"
			if foreign {
				owner = "another-app"
			}
			adds, deletes := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				action := r.Header.Get("SOAPAction")
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(action, "GetExternalIPAddress") && !strings.Contains(string(body), "<NewProtocol>TCP</NewProtocol>") {
					t.Error("не TCP")
				}
				switch {
				case strings.Contains(action, "GetExternalIPAddress"):
					fmt.Fprint(w, "<response><NewExternalIPAddress>8.8.8.8</NewExternalIPAddress></response>")
				case strings.Contains(action, "GetSpecificPortMappingEntry"):
					if !exists {
						w.WriteHeader(500)
						fmt.Fprint(w, "<fault><errorCode>714</errorCode></fault>")
						return
					}
					fmt.Fprintf(w, "<response><NewInternalClient>127.0.0.1</NewInternalClient><NewInternalPort>8443</NewInternalPort><NewPortMappingDescription>%s</NewPortMappingDescription><NewLeaseDuration>600</NewLeaseDuration></response>", owner)
				case strings.Contains(action, "AddPortMapping"):
					adds++
					exists = true
					fmt.Fprint(w, "<response/>")
				case strings.Contains(action, "DeletePortMapping"):
					deletes++
					exists = false
					fmt.Fprint(w, "<response/>")
				default:
					t.Error(action)
				}
			}))
			defer server.Close()
			route := Route{Gateway: netip.MustParseAddrPort("127.0.0.1:5351"), Local: netip.MustParseAddr("127.0.0.1")}
			client := upnpClient{route: route, service: upnpService{Type: "urn:schemas-upnp-org:service:WANIPConnection:1", Control: server.URL}, client: upnpHTTP(route), internal: 8443, external: 9443, marker: "Space-test"}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			mapping, err := client.mapPort(ctx, false)
			if foreign {
				if err == nil {
					t.Fatal("чужое правило заменено")
				}
				if err = client.remove(ctx); err == nil {
					t.Fatal("чужое правило удалено")
				}
				if adds != 0 || deletes != 0 {
					t.Fatal(adds, deletes)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = mapping.Renew(ctx); err != nil {
				t.Fatal(err)
			}
			if err = mapping.Delete(ctx); err != nil {
				t.Fatal(err)
			}
			if adds != 2 || deletes != 1 {
				t.Fatal(adds, deletes)
			}
		})
	}
}

func TestUPnPRejectsUnboundedXMLAndRedirect(t *testing.T) {
	if _, err := soapFields([]byte(strings.Repeat("<x>", 30) + strings.Repeat("</x>", 30))); err == nil {
		t.Fatal("нет бюджета глубины")
	}
	if _, err := soapFields([]byte("<x>" + strings.Repeat("a", 9000) + "</x>")); err == nil {
		t.Fatal("нет бюджета поля")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://example.com/", 302) }))
	defer server.Close()
	route := Route{Gateway: netip.MustParseAddrPort("127.0.0.1:5351"), Local: netip.MustParseAddr("127.0.0.1")}
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	if _, _, err := readUPnP(context.Background(), upnpHTTP(route), req); err == nil {
		t.Fatal("принято перенаправление")
	}
}
