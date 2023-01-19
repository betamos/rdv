package rdv

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

type Config struct {
	// TLS config to use with the rdv server.
	TlsConfig *tls.Config

	// Strategy for choosing the conn to use. If nil, defaults to RelayPenalty(2 * time.Second)
	DialChooser Chooser

	// Defaults to using all available addresses that match `GoodSelfAddr`.
	// This is called on each Dial or Accept, so it should be quick (ideally < 100ms).
	// Can be overridden if port mapping protocols are needed.
	SelfAddrFunc func(ctx context.Context, socket *Socket) []netip.AddrPort

	// Logging function.
	Logf func(string, ...interface{})
}

func (c *Config) logf(format string, v ...interface{}) {
	if c.Logf == nil {
		return
	}
	c.Logf(format+"\n", v...)
}

func (c *Config) dialChooser() Chooser {
	if c.DialChooser != nil {
		return c.DialChooser
	}
	return RelayPenalty(2 * time.Second)
}

func (c *Config) selfAddrs(ctx context.Context, socket *Socket) []netip.AddrPort {
	fn := c.SelfAddrFunc
	if fn == nil {
		fn = DefaultSelfAddrs
	}
	return fn(ctx, socket)
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

func (c *Config) DialContext(ctx context.Context, addr string, token string, reqHeader http.Header) (*Conn, *http.Response, error) {
	return c.do(ctx, newMeta(true, addr, token), reqHeader)
}

func (c *Config) AcceptContext(ctx context.Context, addr string, token string, reqHeader http.Header) (*Conn, *http.Response, error) {
	return c.do(ctx, newMeta(false, addr, token), reqHeader)
}

func (c *Config) Accept(addr string, token string, reqHeader http.Header) (*Conn, *http.Response, error) {
	return c.AcceptContext(context.Background(), addr, token, reqHeader)
}

func (c *Config) Dial(addr string, token string, reqHeader http.Header) (*Conn, *http.Response, error) {
	return c.DialContext(context.Background(), addr, token, reqHeader)
}

func (c *Config) do(ctx context.Context, meta *Meta, reqHeader http.Header) (*Conn, *http.Response, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	socket, err := NewSocket(ctx, 0, c.TlsConfig)
	if err != nil {
		return nil, nil, err
	}
	defer socket.Close()

	var (
		ncs                = make(chan *Conn, 32)
		candidates         = make(chan *Conn, 32)
		chooser    Chooser = lnChoose
	)
	meta.SelfAddrs = c.selfAddrs(ctx, socket)

	relay, resp, err := dialRdvServer(ctx, socket, meta, reqHeader)
	if err != nil {
		return nil, resp, err
	}
	if meta.IsDialer {
		chooser = c.dialChooser()
	}
	ncs <- relay // add relay conn

	c.logf(meta.clientSummary())
	go c.dialAndListen(relay, socket, ncs)
	go c.peerShake(ncs, candidates)
	chosen, unchosen := chooser(cancel, candidates)
	for _, conn := range unchosen {
		c.logf("discard %v", conn.RemoteAddr())
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

func (c *Config) dialAndListen(relay *Conn, s *Socket, ncs chan *Conn) {
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
		if err := AcceptableAddr(addr); err != nil {
			c.logf("dial %v: %v", addr, err)
			continue
		}
		wg.Add(1)
		go func(addr netip.AddrPort) {
			defer wg.Done()
			nc, err := s.DialIPContext(ctx, addr)
			if err != nil {
				c.logf("dial %v: %v", addr, unwrapOp(err))
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
		ncs <- newDirectConn(nc, relay.meta, relay.req)
	}
	wg.Wait()
	close(ncs)
	// success, otherwise relay
}

func (c *Config) peerShake(in chan *Conn, out chan *Conn) {
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
				c.logf("shake %v %v", conn.RemoteAddr(), unwrapOp(err))
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
