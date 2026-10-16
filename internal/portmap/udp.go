package portmap

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/netip"
	"time"
)

var wire = binary.BigEndian

func validLease(seconds uint32) bool { return seconds >= 30 && seconds <= Lifetime }

func createPCP(ctx context.Context, route Route, internal, external uint16) (*Mapping, error) {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	return pcp(ctx, route, internal, external, nonce, Lifetime)
}

func pcp(ctx context.Context, route Route, internal, external uint16, nonce [12]byte, lifetime uint32) (*Mapping, error) {
	packet := make([]byte, 60)
	packet[0], packet[1], packet[36] = 2, 1, 6 // PCP MAP, TCP.
	wire.PutUint32(packet[4:8], lifetime)
	local := route.Local.As16()
	copy(packet[8:24], local[:])
	copy(packet[24:36], nonce[:])
	wire.PutUint16(packet[40:42], internal)
	wire.PutUint16(packet[42:44], external)
	response, err := exchange(ctx, route, packet, 60)
	if err != nil {
		return nil, err
	}
	if response[0] != 2 || response[1] != 129 || response[3] != 0 || response[36] != 6 ||
		!bytes.Equal(response[24:36], nonce[:]) || wire.Uint16(response[40:42]) != internal {
		return nil, fmt.Errorf("PCP: отклонён неподходящий ответ MAP")
	}
	seconds := wire.Uint32(response[4:8])
	if lifetime == 0 {
		if seconds != 0 {
			return nil, fmt.Errorf("PCP: удаление не подтверждено")
		}
		return nil, nil
	}
	ip := netip.AddrFrom16([16]byte(response[44:60])).Unmap()
	port := wire.Uint16(response[42:44])
	if !validLease(seconds) || !PublicIP(ip) || !ip.Is4() || port == 0 {
		if seconds > 0 && port != 0 {
			_, _ = pcp(ctx, route, internal, port, nonce, 0)
		}
		return nil, fmt.Errorf("PCP: непубличный адрес или неверная аренда")
	}
	return &Mapping{External: netip.AddrPortFrom(ip, port), Protocol: "PCP", Lifetime: time.Duration(seconds) * time.Second,
		Renew:  func(ctx context.Context) (*Mapping, error) { return pcp(ctx, route, internal, port, nonce, Lifetime) },
		Delete: func(ctx context.Context) error { _, err := pcp(ctx, route, internal, port, nonce, 0); return err },
	}, nil
}

func createPMP(ctx context.Context, route Route, internal, external uint16) (*Mapping, error) {
	return pmp(ctx, route, internal, external, Lifetime)
}

func pmp(ctx context.Context, route Route, internal, external uint16, lifetime uint32) (*Mapping, error) {
	var ip netip.Addr
	if lifetime != 0 {
		address, err := exchange(ctx, route, []byte{0, 0}, 12)
		if err != nil {
			return nil, err
		}
		ip = netip.AddrFrom4([4]byte(address[8:12]))
		if address[0] != 0 || address[1] != 128 || wire.Uint16(address[2:4]) != 0 || !PublicIP(ip) {
			return nil, fmt.Errorf("NAT-PMP: нет публичного адреса")
		}
	}
	packet := make([]byte, 12)
	packet[1] = 2 // NAT-PMP TCP.
	wire.PutUint16(packet[4:6], internal)
	wire.PutUint16(packet[6:8], external)
	wire.PutUint32(packet[8:12], lifetime)
	response, err := exchange(ctx, route, packet, 16)
	if err != nil {
		return nil, err
	}
	seconds := wire.Uint32(response[12:16])
	port := wire.Uint16(response[10:12])
	if response[0] != 0 || response[1] != 130 || wire.Uint16(response[2:4]) != 0 || wire.Uint16(response[8:10]) != internal {
		return nil, fmt.Errorf("NAT-PMP: отклонён неподходящий ответ")
	}
	if lifetime == 0 {
		if seconds != 0 {
			return nil, fmt.Errorf("NAT-PMP: удаление не подтверждено")
		}
		return nil, nil
	}
	if !validLease(seconds) || port == 0 {
		if seconds > 0 && port != 0 {
			_, _ = pmp(ctx, route, internal, port, 0)
		}
		return nil, fmt.Errorf("NAT-PMP: неверная аренда")
	}
	return &Mapping{External: netip.AddrPortFrom(ip, port), Protocol: "NAT-PMP", Lifetime: time.Duration(seconds) * time.Second,
		Renew:  func(ctx context.Context) (*Mapping, error) { return pmp(ctx, route, internal, port, Lifetime) },
		Delete: func(ctx context.Context) error { _, err := pmp(ctx, route, internal, port, 0); return err },
	}, nil
}
