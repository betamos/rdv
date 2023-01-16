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
		if GoodSelfAddr(addr) == nil {
			addrs = append(addrs, netip.AddrPortFrom(addr.Addr(), socket.Port))
		}
	}
	return addrs
}

func AcceptableAddr(addr netip.AddrPort) error {
	if addr.Port() < 1024 {
		// Let's not dial any system ports
		return ErrPrivilegedPort
	}
	ip := addr.Addr()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() {
		return ErrInvalidAddr
	}
	return nil
}

func GoodSelfAddr(addr netip.AddrPort) error {
	if err := AcceptableAddr(addr); err != nil {
		return err
	}
	ip := addr.Addr()
	if ip.Is6() && ip.IsPrivate() || !ip.IsGlobalUnicast() {
		// Disable due to marginal chances of increased connectivity
		return ErrDontUse
	}
	return nil
}

func GoodObservedAddr(addr netip.AddrPort) error {
	if err := AcceptableAddr(addr); err != nil {
		return err
	}
	ip := addr.Addr()
	if ip.Is6() || ip.IsPrivate() || ip.IsLoopback() {
		// Disable due to marginal chances of increased connectivity
		return ErrDontUse
	}
	return nil
}
