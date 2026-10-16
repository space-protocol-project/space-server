package portmap

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

func fakeUDP(t *testing.T, reply func([]byte) []byte) Route {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		for {
			buffer := make([]byte, 128)
			n, from, err := conn.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			response := reply(buffer[:n])
			if response != nil {
				_, _ = conn.WriteToUDP(response, from)
			}
		}
	}()
	return Route{Gateway: conn.LocalAddr().(*net.UDPAddr).AddrPort(), Local: netip.MustParseAddr("127.0.0.1")}
}

func TestPCPLifecycle(t *testing.T) {
	requests := make(chan []byte, 8)
	route := fakeUDP(t, func(packet []byte) []byte {
		requests <- append([]byte(nil), packet...)
		response := append([]byte(nil), packet...)
		response[1] = 129
		ip := netip.MustParseAddr("8.8.8.8").As16()
		copy(response[44:60], ip[:])
		wire.PutUint16(response[42:44], 9443)
		return response
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	mapping, err := createPCP(ctx, route, 8443, 9443)
	if err != nil {
		t.Fatal(err)
	}
	if mapping.External.String() != "8.8.8.8:9443" || mapping.Lifetime != 600*time.Second {
		t.Fatal(mapping)
	}
	renewed, err := mapping.Renew(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = renewed.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	first, second, last := <-requests, <-requests, <-requests
	if string(first[24:36]) != string(second[24:36]) || string(first[24:36]) != string(last[24:36]) {
		t.Fatal("nonce изменён")
	}
	if wire.Uint32(last[4:8]) != 0 || wire.Uint16(last[40:42]) != 8443 || last[36] != 6 {
		t.Fatal("неадресное удаление")
	}
}

func TestPCPRejectsUnboundReply(t *testing.T) {
	for _, mutation := range []string{"nonce", "port", "opcode", "size", "private"} {
		t.Run(mutation, func(t *testing.T) {
			route := fakeUDP(t, func(packet []byte) []byte {
				response := append([]byte(nil), packet...)
				response[1] = 129
				ip := netip.MustParseAddr("8.8.8.8").As16()
				copy(response[44:60], ip[:])
				switch mutation {
				case "nonce":
					response[24] ^= 1
				case "port":
					response[40] ^= 1
				case "opcode":
					response[1] = 128
				case "size":
					response = response[:59]
				case "private":
					ip = netip.MustParseAddr("192.168.1.2").As16()
					copy(response[44:60], ip[:])
				}
				return response
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := createPCP(ctx, route, 8443, 9443); err == nil {
				t.Fatal("неподходящий ответ принят")
			}
		})
	}
}

func TestPMPLifecycle(t *testing.T) {
	requests := make(chan []byte, 8)
	route := fakeUDP(t, func(packet []byte) []byte {
		requests <- append([]byte(nil), packet...)
		if len(packet) == 2 {
			return []byte{0, 128, 0, 0, 0, 0, 0, 1, 8, 8, 8, 8}
		}
		response := make([]byte, 16)
		response[1] = 130
		copy(response[8:12], packet[4:8])
		copy(response[12:16], packet[8:12])
		return response
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	mapping, err := createPMP(ctx, route, 8443, 9443)
	if err != nil {
		t.Fatal(err)
	}
	mapping, err = mapping.Renew(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = mapping.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		<-requests
	}
	last := <-requests
	if wire.Uint16(last[4:6]) != 8443 || wire.Uint32(last[8:12]) != 0 || last[1] != 2 {
		t.Fatal("удалено чужое правило")
	}
}

func TestPublicIPRejectsSharedAndReserved(t *testing.T) {
	for _, ip := range []string{"100.64.0.1", "192.168.1.1", "127.0.0.1", "0.0.0.0", "203.0.113.4", "198.18.0.1", "255.255.255.255", "::1"} {
		if PublicIP(netip.MustParseAddr(ip)) {
			t.Fatal(ip)
		}
	}
	if !PublicIP(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("публичный адрес отклонён")
	}
}
