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
func DefaultSelfAddrsOld(ctx context.Context, socket *Socket) []netip.AddrPort {
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

const (
	v4pub = "8.8.8.8:80"
	v6pub = "[2001:4860:4860::8888]:80"
)

// Get the ipv4+ipv6 addrs for the default routes only, max 2.
func DefaultRouteSelfAddrs(ctx context.Context, socket *Socket) (addrs []netip.AddrPort) {
	// TODO: We might need to set these laddrs on the socket dialers,
	// in case we get a different outbound ipv6 addr for the tcp conn.

	// TODO: We could get an addr from the interface instead for multipath..
	// However, we need to avoid (or at least not prefer) Unicast Local Addrs (fd00::/8).
	v4, _ := localNetIpFor(v4pub)
	v6, _ := localNetIpFor(v6pub)
	addrs = append(addrs, netip.AddrPortFrom(v4, socket.Port))
	addrs = append(addrs, netip.AddrPortFrom(v6, socket.Port))
	return addrs
}

func DefaultSelfAddrs(ctx context.Context, socket *Socket) (addrs []netip.AddrPort) {
	for _, addr := range socket.Interface.FirstAddrs() {
		addrs = append(addrs, netip.AddrPortFrom(addr.Addr(), socket.Port))
	}
	return addrs
}

// Get local ip for an outbound conn, without actually sending any traffic.
func localNetIpFor(ips string) (netip.Addr, error) {
	ip, _ := localIpFor(ips)
	addr, _ := netip.AddrFromSlice(ip)
	return addr.Unmap(), nil
}

func localIpFor(ip string) (net.IP, error) {
	conn, err := net.Dial("udp", ip)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP, nil
}

func localIsPubliclyRoutable(addr netip.Addr) bool {
	d := &net.Dialer{
		LocalAddr: &net.UDPAddr{IP: addr.AsSlice(), Port: 42400},
	}
	dst := v4pub
	if addr.Is6() {
		dst = v6pub
	}
	_, err := d.Dial("udp", dst)
	return err == nil
}

// An IP address space is derived from an IP address. These are used for connectivity in rdv, and
// thus don't include multicast etc. order to differentiate between meaningful addrs.
type AddrSpace uint32

const (

	// Denotes an invalid, or none-space.
	SpaceInvalid AddrSpace = 0

	// Public IPv4 addrs, extremely common and useful for remote connectivity when available.
	SpacePublic4 AddrSpace = 1 << iota

	// Public IPv6 addrs, very common and very useful for both local and remote connectivity.
	SpacePublic6

	// Private IPv4 addrs are very common and useful for local connectivity.
	SpacePrivate4

	// ULA ipv6 addrs are not common (although link-local are).
	SpacePrivate6

	// Link-local ipv4 addrs are not common in most setups.
	SpaceLink4

	// Link-local ipv6 addrs are not recommended with rdv due to zones.
	SpaceLink6

	// Loopback addresses are mostly useful for testing.
	SpaceLoopback
)

const (
	// NoSpaces won't match any spaces
	NoSpaces AddrSpace = 1 << 31

	// Public IPs only
	PublicSpaces AddrSpace = SpacePublic4 | SpacePublic6

	// Sensible defaults for most users, includes private and public spaces
	DefaultSpaces AddrSpace = SpacePublic4 | SpacePublic6 | SpacePrivate4 | SpacePrivate6

	// All IP spaces
	AllSpaces AddrSpace = ^NoSpaces
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
	case SpaceLoopback:
		return "loopback"
	}
	return "none"
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
