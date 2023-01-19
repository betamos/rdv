package rdv

import (
	"fmt"
	"net/netip"
)

type Meta struct {
	ServerAddr           string
	IsDialer             bool
	Token                string
	ObservedAddr         *netip.AddrPort
	SelfAddrs, PeerAddrs []netip.AddrPort
}

func newMeta(isDialer bool, addr string, token string) *Meta {
	return &Meta{IsDialer: isDialer, Token: token, ServerAddr: addr}
}

// e.g. `accept abc *:46835 -> 192.168.1.16:38289, 172.17.0.1:38289`
func (m *Meta) clientSummary() string {
	method := "accept"
	if m.IsDialer {
		method = "dial"
	}
	return fmt.Sprintf("%s %s %v (%v) -> %v", method, m.Token, formatAddrs(m.SelfAddrs), m.ObservedAddr, formatAddrs(m.PeerAddrs))
}

// e.g. `accept abc 22.22.22.22:12345`
func (m *Meta) serverSummary() string {
	method := "accept"
	if m.IsDialer {
		method = "dial"
	}
	return fmt.Sprintf("%s %s %v", method, m.Token, m.ObservedAddr)
}

func (m *Meta) setPeerAddrsFrom(peer *Meta) {
	m.PeerAddrs = make([]netip.AddrPort, len(peer.SelfAddrs), len(peer.SelfAddrs)+1)
	copy(m.PeerAddrs, peer.SelfAddrs)

	if peer.ObservedAddr != nil {
		m.PeerAddrs = append(m.PeerAddrs, *peer.ObservedAddr)
	}
}
