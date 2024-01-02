package rdv

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

type ClientConfig struct {
	// TLS config to use with the rdv server.
	TlsConfig *tls.Config

	// Strategy for choosing the conn to use. If nil, defaults to RelayPenalty(time.Second)
	DialChooser Chooser

	// Can be used to allow only a certain set of spaces, such as public IPs only. By default
	// DefaultSpaces which optimal for both local and global peering.
	AddrSpaces AddrSpace

	// Defaults to using all available interface addresses. The list is automatically filtered by
	// AddrSpaces. This is called on each Dial or Accept, so it should be quick (ideally < 100ms).
	// Can be overridden if port mapping protocols are needed.
	SelfAddrFunc func(ctx context.Context, socket *Socket) []netip.AddrPort

	// Logger, by default slog.Default()
	Logger *slog.Logger
}

func (c *ClientConfig) setDefaults() {
	if c.DialChooser == nil {
		c.DialChooser = RelayPenalty(time.Second)
	}
	if c.AddrSpaces == 0 {
		c.AddrSpaces = DefaultSpaces
	}
	if c.SelfAddrFunc == nil {
		c.SelfAddrFunc = DefaultSelfAddrs
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

type Client struct {
	cfg ClientConfig
}

func NewClient(cfg *ClientConfig) *Client {
	c := &Client{}

	if cfg != nil {
		c.cfg = *cfg
	}
	c.cfg.setDefaults()
	return c
}

// Chooser is called once a direct connection is started.
// All conns on lobby are ready to go
// The chan is closed when either:
// - The parents timeout is reached
// - The parents context is canceled
// - The picker calls the cancel function (optional)
// The picker must drain the lobby channel.
// The picker must return all conns.
// Chosen may be nil
type Chooser func(cancel func(), candidates chan *Conn) (chosen *Conn, unchosen []*Conn)

// A chooser which gives the relay some penalty
// How long the dialer waits for a p2p connection, before falling back on using the relay.
// If zero, the relay is used as soon as available, but p2p can still be faster.
// A larger value increases the chances of p2p, at the cost of delaying the connection.
// If exceeding ConnTimeout, the relay will not be used.
func RelayPenalty(penalty time.Duration) Chooser {
	return func(cancel func(), candidates chan *Conn) (chosen *Conn, unchosen []*Conn) {
		return withRelayPenalty(cancel, candidates, penalty)
	}
}

func withRelayPenalty(cancel func(), candidates chan *Conn, penalty time.Duration) (chosen *Conn, unchosen []*Conn) {
	timer := time.AfterFunc(time.Hour, cancel)
	defer timer.Stop()
	for nc := range candidates {
		if !nc.IsRelay() {
			cancel()
		} else {
			timer.Reset(penalty)
		}
		if chosen == nil {
			chosen = nc
		} else if chosen.IsRelay() {
			// Unchoose the relay conn in favor of the direct conn
			unchosen = append(unchosen, chosen)
			chosen = nc
		} else {
			unchosen = append(unchosen, nc)
		}
	}
	return
}

// Chooser for listener, which always returns the first
func lnChoose(cancel func(), candidates chan *Conn) (chosen *Conn, unchosen []*Conn) {
	chosen = <-candidates
	cancel()
	for nc := range candidates {
		unchosen = append(unchosen, nc)
	}
	return
}

func (c *Client) Dial(ctx context.Context, addr string, token string, reqHeader http.Header) (*Conn, *http.Response, error) {
	return c.do(ctx, newMeta(true, addr, token), reqHeader)
}

func (c *Client) Accept(ctx context.Context, addr string, token string, reqHeader http.Header) (*Conn, *http.Response, error) {
	return c.do(ctx, newMeta(false, addr, token), reqHeader)
}

func (c *Client) do(ctx context.Context, meta *Meta, reqHeader http.Header) (*Conn, *http.Response, error) {
	log := c.cfg.Logger.With("token", meta.Token)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	socket, err := NewSocket(ctx, 0, c.cfg.TlsConfig)
	if err != nil {
		return nil, nil, err
	}
	defer socket.Close()

	var (
		ncs                = make(chan *Conn, 32)
		candidates         = make(chan *Conn, 32)
		chooser    Chooser = lnChoose
	)
	selfAddrs := c.cfg.SelfAddrFunc(ctx, socket)
	meta.SelfAddrs = filter(selfAddrs, func(addr netip.AddrPort) bool {
		return c.cfg.AddrSpaces.Includes(GetAddrSpace(addr.Addr()))
	})

	relay, resp, err := dialRdvServer(ctx, socket, meta, reqHeader)
	if err != nil {
		return nil, resp, err
	}
	if meta.IsDialer {
		chooser = c.cfg.DialChooser
	}
	ncs <- relay // add relay conn

	log.Debug("rdv client: connecting to peer", "observed", meta.ObservedAddr, "self_addrs", meta.SelfAddrs, "peer_addrs", meta.PeerAddrs)
	go dialAndListen(log, c.cfg.AddrSpaces, relay, socket, ncs)
	go peerShake(log, ncs, candidates)
	chosen, unchosen := chooser(cancel, candidates)
	for _, conn := range unchosen {
		log.Debug("rdv client: closing unchosen", "addr", conn.RemoteAddr())
		conn.Close()
	}
	if chosen == nil {
		return nil, nil, ErrNotChosen
	}
	chosen.SetDeadline(verySoon())
	err = chosen.clientShake()
	if err != nil {
		chosen.Close()
		return nil, nil, err
	}
	chosen.SetDeadline(time.Time{})
	return chosen, nil, nil
}

func dialAndListen(log *slog.Logger, spaces AddrSpace, relay *Conn, s *Socket, ncs chan *Conn) {
	var (
		wg sync.WaitGroup
	)
	ctx := relay.req.Context()
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		s.Close()
	}()
	for _, addr := range relay.meta.PeerAddrs {
		space := GetAddrSpace(addr.Addr())
		if !spaces.Includes(space) { // TODO: Perhaps log the addr space
			log.Debug("rdv client: skip outbound", "addr", addr, "space", space)
			continue
		}
		wg.Add(1)
		go func(addr netip.AddrPort) {
			defer wg.Done()
			nc, err := s.DialIPContext(ctx, addr)
			if err != nil {
				log.Debug("rdv client: dial failed", "addr", addr, "err", unwrapOp(err))
				return
			}
			ncs <- newDirectConn(nc, relay.meta, relay.req)
		}(addr)
	}
	for {
		nc, err := s.Accept()
		if err != nil {
			break
		}
		addr, space := FromNetAddr(nc.RemoteAddr())
		if err != nil || !spaces.Includes(space) {
			log.Debug("rdv client: close inbound", "addr", addr, "err", errors.New("disabled addr space"))
			nc.Close()
			continue // Log error
		}
		ncs <- newDirectConn(nc, relay.meta, relay.req)
	}
	wg.Wait()
	close(ncs)
	// success, otherwise relay
}

func peerShake(log *slog.Logger, in chan *Conn, out chan *Conn) {
	var (
		cArr = []net.Conn{}
		wg   sync.WaitGroup
	)
	for conn := range in {
		cArr = append(cArr, conn)
		wg.Add(1)
		go func(conn *Conn) {
			defer wg.Done()
			err := conn.clientHand()
			if err != nil {
				log.Debug("rdv client: shake failed", "addr", conn.RemoteAddr(), "err", unwrapOp(err))
				conn.Close()
				return
			}
			out <- conn
		}(conn)
	}

	// Expire all deadlines to trigger a
	t := past()
	for _, c := range cArr {
		c.SetDeadline(t)
	}
	wg.Wait()
	close(out)
}
