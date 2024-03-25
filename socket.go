package rdv

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	urlpkg "net/url"

	"github.com/libp2p/go-reuseport"
)

// An SO_REUSEPORT TCP socket suitable for NAT traversal/hole punching, over both ipv4 and ipv6.
// Usually, higher level abstractions should be used.
type Socket struct {

	// A dual-stack (ipv4/6) TCP listener.
	//
	// TODO: Should this be refactored into two single-stack listeners, in order to support
	// non dual-stack systems? And if so, can the ports be different? See also NAT64.
	net.Listener

	/// Dialers for ipv4 and ipv6.
	D4, D6 *net.Dialer

	/// Port number for the socket, both stacks.
	Port uint16

	// TLS config for https.
	//
	// TODO: Higher level protocols should be one layer above sockets?
	TlsConfig *tls.Config

	// The interface to use
	Interface *Interface
}

func dialer(localIp netip.Addr, port uint16) *net.Dialer {
	return &net.Dialer{
		Control:   reuseport.Control,
		LocalAddr: &net.TCPAddr{IP: localIp.AsSlice(), Port: int(port)},
	}
}

func NewSocket(ctx context.Context, iface *Interface, port uint16, tlsConf *tls.Config) (*Socket, error) {
	lc := net.ListenConfig{
		Control: reuseport.Control,
	}
	ln, err := lc.Listen(ctx, "tcp", fmt.Sprintf(":%v", port))
	if err != nil {
		return nil, err
	}
	slog.Info("socket", "laddr", ln.Addr())
	port = netip.MustParseAddrPort(ln.Addr().String()).Port()
	return &Socket{
		Listener:  ln,
		D4:        dialer(netip.IPv4Unspecified(), port),
		D6:        dialer(netip.IPv6Unspecified(), port),
		Port:      port,
		TlsConfig: tlsConf,
		Interface: iface,
	}, nil
}

// Local addrs for dialing
func (s *Socket) SetLocalAddrs(v4, v6 netip.Addr) {
	s.D4 = dialer(v4, s.Port)
	s.D6 = dialer(v6, s.Port)
}

func (s *Socket) networkToDialer(network string) *net.Dialer {
	if network == "tcp6" {
		return s.D6
	}
	return s.D4
}

func (s *Socket) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d := s.networkToDialer(network)
	return d.DialContext(ctx, network, address)
}

func (s *Socket) DialIPContext(ctx context.Context, addr netip.AddrPort) (net.Conn, error) {
	laddr := netip.IPv4Unspecified()
	if addr.Addr().Is6() {
		laddr = netip.IPv6Unspecified()
	}

	iaddrs := s.Interface.SpaceMap()
	space := GetAddrSpace(addr.Addr())

	// I.e. if addr is public6, then use the first public6 addr.
	// TODO: If public4 -> use private4 laddr. For now, we can leave unspecified
	if laddrs, ok := iaddrs[space]; ok {
		laddr = laddrs[0].Addr()
	}

	d := dialer(laddr, s.Port)
	return d.DialContext(ctx, "tcp", addr.String())
}

func (s *Socket) DialURLContext(ctx context.Context, network string, url *urlpkg.URL) (net.Conn, error) {
	hostPort := net.JoinHostPort(url.Hostname(), urlPort(url))
	netd := s.networkToDialer(network)
	dialFn := netd.DialContext
	if url.Scheme == "https" {
		tlsd := &tls.Dialer{
			NetDialer: netd,
			Config:    s.TlsConfig,
		}
		dialFn = tlsd.DialContext
	} else if url.Scheme != "http" {
		return nil, fmt.Errorf("unexpected scheme [%s]", url.Scheme)
	}
	return dialFn(ctx, network, hostPort)
}
