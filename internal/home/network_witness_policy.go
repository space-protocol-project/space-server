package home

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"time"
)

type witnessTarget struct {
	endpoint  Endpoint
	addresses []netip.Addr
}
type witnessPolicy struct {
	targets []witnessTarget
	own     []netip.Addr
	valid   bool
}

var witnessReserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"),
	// Не принимаем IPv4-переходы за независимую IPv6-сеть.
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("2001::/32"),
}

func publicWitnessIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.Zone() != "" {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range witnessReserved {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func separateWitnessNetworks(peer, own netip.Addr) bool {
	bits := 24
	if peer.Is6() {
		bits = 48
	}
	return peer.Is4() != own.Is4() || !netip.PrefixFrom(own, bits).Masked().Contains(peer)
}

func resolveWitnessPolicy(parent context.Context, endpoints []Endpoint) witnessPolicy {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	policy := witnessPolicy{valid: true}
	for _, endpoint := range endpoints {
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", endpoint.Host)
		if err != nil || len(addresses) > 32 {
			policy.valid = false
			return policy
		}
		valid := []netip.Addr{}
		for _, ip := range addresses {
			ip = ip.Unmap()
			policy.own = append(policy.own, ip)
			if publicWitnessIP(ip) {
				valid = append(valid, ip)
			}
		}
		policy.targets = append(policy.targets, witnessTarget{endpoint: endpoint, addresses: valid})
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		policy.valid = false
		return policy
	}
	for _, address := range addresses {
		if prefix, err := netip.ParsePrefix(address.String()); err == nil {
			policy.own = append(policy.own, prefix.Addr().Unmap())
		}
	}
	return policy
}

func (p witnessPolicy) externalOrigin(authority, remote, serverName string) string {
	if !p.valid {
		return ""
	}
	peer, err := netip.ParseAddrPort(remote)
	if err != nil || !publicWitnessIP(peer.Addr()) {
		return ""
	}
	for _, own := range p.own {
		if !separateWitnessNetworks(peer.Addr().Unmap(), own.Unmap()) {
			return ""
		}
	}
	endpoint, err := EndpointFromOrigin("https://" + strings.ToLower(authority))
	if err != nil {
		return ""
	}
	if serverName != "" && !strings.EqualFold(serverName, endpoint.Host) {
		return ""
	}
	for _, target := range p.targets {
		if target.endpoint.Origin() == endpoint.Origin() && len(target.addresses) > 0 {
			return endpoint.Origin()
		}
	}
	return ""
}
