// Package turnfixture предоставляет только loopback-ретранслятор для проверок.
// Рабочий runtime его не запускает и не открывает TURN без настройки владельца.
package turnfixture

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"sync/atomic"

	"github.com/pion/turn/v5"
)

type Fixture struct {
	server           *turn.Server
	Secret, UDP, TCP string
	Allocations      atomic.Int64
}

func Start() (*Fixture, error) {
	return StartOn("127.0.0.1")
}

// StartOn разрешает только локальный/частный интерфейс стенда. Нативный
// Windows WebRTC не отправляет UDP с LAN-интерфейса на loopback TURN.
func StartOn(address string) (*Fixture, error) {
	ip := net.ParseIP(address)
	if ip == nil || (!ip.IsLoopback() && !ip.IsPrivate()) {
		return nil, fmt.Errorf("test TURN requires a private interface")
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, err
	}
	udp, err := net.ListenPacket("udp4", net.JoinHostPort(address, "0"))
	if err != nil {
		return nil, err
	}
	tcp, err := net.Listen("tcp4", net.JoinHostPort(address, "0"))
	if err != nil {
		_ = udp.Close()
		return nil, err
	}
	f := &Fixture{Secret: hex.EncodeToString(secret[:]), UDP: "turn:" + udp.LocalAddr().String() + "?transport=udp", TCP: "turn:" + tcp.Addr().String() + "?transport=tcp"}
	generator := func() *turn.RelayAddressGeneratorStatic {
		return &turn.RelayAddressGeneratorStatic{RelayAddress: ip, Address: address}
	}
	permission := func(_ net.Addr, peer net.IP) bool { return peer.Equal(ip) }
	f.server, err = turn.NewServer(turn.ServerConfig{
		Realm:             "space-voice-check",
		AuthHandler:       turn.LongTermTURNRESTAuthHandler(f.Secret, nil),
		PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: udp, RelayAddressGenerator: generator(), PermissionHandler: permission}},
		ListenerConfigs:   []turn.ListenerConfig{{Listener: tcp, RelayAddressGenerator: generator(), PermissionHandler: permission}},
		EventHandler:      turn.EventHandler{OnAllocationCreated: func(net.Addr, net.Addr, string, string, string, net.Addr, int) { f.Allocations.Add(1) }},
	})
	if err != nil {
		_ = udp.Close()
		_ = tcp.Close()
		return nil, err
	}
	return f, nil
}

func (f *Fixture) Close() error { return f.server.Close() }
