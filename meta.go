package rdv

import (
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

func (m *Meta) setPeerAddrsFrom(peer *Meta) {
	m.PeerAddrs = make([]netip.AddrPort, len(peer.SelfAddrs), len(peer.SelfAddrs)+1)
	copy(m.PeerAddrs, peer.SelfAddrs)

	if peer.ObservedAddr != nil {
		m.PeerAddrs = append(m.PeerAddrs, *peer.ObservedAddr)
	}
}
