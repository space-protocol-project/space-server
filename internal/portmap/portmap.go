// Package portmap управляет только временной TCP-арендой на локальном шлюзе.
package portmap

import (
	"context"
	"fmt"
	"github.com/jackpal/gateway"
	"net"
	"net/netip"
	"time"
)

const Lifetime = 600

type Route struct {
	Gateway netip.AddrPort
	Local   netip.Addr
}
type Mapping struct {
	External netip.AddrPort
	Protocol string
	Lifetime time.Duration
	Renew    func(context.Context) (*Mapping, error)
	Delete   func(context.Context) error
}

func DefaultRoute() (Route, error) {
	gw, err := gateway.DiscoverGateway()
	if err != nil {
		return Route{}, err
	}
	local, err := gateway.DiscoverInterface()
	if err != nil {
		return Route{}, err
	}
	g, gok := netip.AddrFromSlice(gw.To4())
	l, lok := netip.AddrFromSlice(local.To4())
	if !gok || !lok || !g.IsPrivate() || !l.IsPrivate() {
		return Route{}, fmt.Errorf("Нет локального IPv4-шлюза для проброса порта")
	}
	return Route{netip.AddrPortFrom(g, 5351), l}, nil
}

func PublicIP(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, block := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		if netip.MustParsePrefix(block).Contains(ip) {
			return false
		}
	}
	return true
}

func Create(ctx context.Context, route Route, internal, external uint16) (*Mapping, error) {
	if internal == 0 || external == 0 {
		return nil, fmt.Errorf("Нужен ненулевой TCP-порт")
	}
	var failures []error
	for _, attempt := range []func(context.Context, Route, uint16, uint16) (*Mapping, error){createPCP, createPMP, createUPnP} {
		attemptCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		mapping, err := attempt(attemptCtx, route, internal, external)
		cancel()
		if err == nil {
			return mapping, nil
		}
		failures = append(failures, err)
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("Шлюз не предоставил временный проброс TCP: %v", failures)
}

func exchange(ctx context.Context, route Route, request []byte, size int) ([]byte, error) {
	dialer := net.Dialer{LocalAddr: &net.UDPAddr{IP: net.IP(route.Local.AsSlice())}}
	conn, err := dialer.DialContext(ctx, "udp4", route.Gateway.String())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline := time.Now().Add(2 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	for _, wait := range []time.Duration{400 * time.Millisecond, 800 * time.Millisecond, time.Second} {
		until := time.Now().Add(wait)
		if until.After(deadline) {
			until = deadline
		}
		_ = conn.SetDeadline(until)
		if _, err = conn.Write(request); err != nil {
			return nil, err
		}
		reply := make([]byte, size+1)
		n, readErr := conn.Read(reply)
		if readErr == nil {
			if n != size {
				return nil, fmt.Errorf("Некорректный размер ответа шлюза")
			}
			return reply[:n], nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, readErr
		}
		err = readErr
	}
	return nil, err
}
