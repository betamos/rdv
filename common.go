package rdv

import (
	"context"
	"errors"
	"net"
	"net/netip"
)

const (
	maxAddrs = 10

	protocolName = "rdv/1"

	// Token for this rdv conn, chosen by a client. Request and response.
	hToken = "Rdv-Token"

	// Comma-separated list of self-reported ip:port addrs. Request only.
	hSelfAddrs = "Rdv-Self-Addrs"

	// A comma-separate list of observed and self-reported ip:port addrs of the peer. Response only.
	hPeerAddrs = "Rdv-Peer-Addrs"

	// Observed public ipv4:port addr of the requesting client, from the server's point of view.
	// Response only.
	hObservedAddr = "Rdv-Observed-Addr"
)

var (
	ErrHijackFailed   = errors.New("failed hijacking http conn")
	ErrBadHandshake   = errors.New("bad http handshake")
	ErrProtocol       = errors.New("rdv protocol error")
	ErrUpgrade        = errors.New("rdv http upgrade error")
	ErrNotChosen      = errors.New("no rdv conn chosen")
	ErrServerClosed   = errors.New("rdv server closed")
	ErrPrivilegedPort = errors.New("bad addr: expected port >=1024")
	ErrInvalidAddr    = errors.New("bad addr: invalid addr")
	ErrDontUse        = errors.New("bad addr: not helpful for connectivity")
)

// TODO: Ipv4-mapped v6-addrs
func DefaultSelfAddrs(ctx context.Context, socket *Socket) []netip.AddrPort {
	netAddrs, _ := net.InterfaceAddrs()
	var addrs []netip.AddrPort
	for _, netAddr := range netAddrs {
		if len(addrs) > maxAddrs-1 { // save one addr for observed addr
			break
		}
		prefixAddr := netip.MustParsePrefix(netAddr.String())
		addr := netip.AddrPortFrom(prefixAddr.Addr(), socket.Port)
		addrs = append(addrs, addr)
	}
	return addrs
}

// IP address space, in order to differentiate between meaningful addrs.
// Link-local ipv6 addrs are not recommended with rdv due to zones.
type AddrSpace uint32

const (
	SpaceInvalid AddrSpace = 0
	SpacePublic4           = 1 << iota
	SpacePublic6
	SpacePrivate4
	SpacePrivate6
	SpaceLink4
	SpaceLink6
	SpaceLoopback
)

// Provides connectivity in almost all cases.
// Private v6 is disabled for noise - almost all private networks have ipv4 support.
const (
	PublicSpaces  AddrSpace = SpacePublic4 | SpacePublic6
	DefaultSpaces AddrSpace = SpacePublic4 | SpacePublic6 | SpacePrivate4
	AllSpaces     AddrSpace = ^SpaceInvalid //SpacePublic4 | SpacePublic6 | SpacePrivate4 | SpacePrivate6 | SpaceLink4 | SpaceLink6 | SpaceLoopback
)

func (s AddrSpace) Includes(space AddrSpace) bool {
	return space&s != 0
}

func (s AddrSpace) String() string {
	switch s {
	case SpacePublic4:
		return "public4"
	case SpacePublic6:
		return "public6"
	case SpacePrivate4:
		return "private4"
	case SpacePrivate6:
		return "private6"
	case SpaceLink4:
		return "link4"
	case SpaceLink6:
		return "link6"
	}
	return "invalid"
}

// Get AddrPort and AddrSpace from a TCP net.Addr
func FromNetAddr(na net.Addr) (addr netip.AddrPort, space AddrSpace) {
	addr, _ = netip.ParseAddrPort(na.String())
	space = GetAddrSpace(addr.Addr())
	return
}

func GetAddrSpace(ip netip.Addr) AddrSpace {
	// TODO: Check what to do about ipv4-mapped ipv6 addresses
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() {
		return SpaceInvalid
	}
	if ip.IsLoopback() {
		return SpaceLoopback
	}
	if ip.IsLinkLocalUnicast() {
		if ip.Is4() {
			return SpaceLink4
		}
		return SpaceLink6
	}
	if ip.IsPrivate() {
		if ip.Is4() {
			return SpacePrivate4
		}
		return SpacePrivate6
	}
	if ip.IsGlobalUnicast() {
		if ip.Is4() {
			return SpacePublic4
		}
		return SpacePublic6
	}
	return SpaceInvalid
}
