package rdv

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"sync"
	"time"
)

type ServerConfig struct {
	// Amount of time that on peer can wait in the lobby for its partner. Zero means no timeout.
	LobbyTimeout time.Duration

	// Amount of inactivity before relay conns are dropped. Zero means no timeout.
	// This can be overridden with a custom RelayConfig in ServeFunc.
	RelayTimeout time.Duration

	// Number of peers that are allowed to wait in the lobby simultaneous. Zero means no limit.
	MaxLobbyConns int

	// Number of active relay conns, each using a pair of underlying network connections.
	// Conns that end up using p2p are treated as relay conns until they establish connectivity.
	// Zero means no limit.
	MaxRelayConns int

	// Function to serve a relay connection between dialer and server.
	// The provided context is canceled along with the server.
	// The function is responsible for closing the connections.
	// Used to customize monitoring, rate limiting, idle timeouts relating to relay
	// connections. See RelayConfig for defaults.
	ServeFunc func(ctx context.Context, dc, ac *Conn)

	// Determines the remote addr:port from the client request, and adds it to the set of
	// candidate addrs sent to the other peer. If nil, `req.RemoteAddr` is used.
	// If your server is behind a load balancer, reverse proxy or similar, you may need to extract
	// the address using forwarding headers. To disable this feature, return "". See the server
	// setup guide for details.
	ObservedAddrFunc func(req *http.Request) (netip.AddrPort, error)

	// Logging fn
	Logf func(string, ...interface{})
}

var DefaultServerConfig = &ServerConfig{
	LobbyTimeout: 20 * time.Second,
	RelayTimeout: 20 * time.Second,
	ServeFunc:    DefaultHandler,
}

type Server struct {
	cfg    *ServerConfig
	idle   map[string]*Conn
	connCh chan *Conn // Incoming upgraded conns: request redeivec, no response sent, no deadline

	monCh chan string // token sent when current conn mapping is complete

	// Guards connCh because Go's HTTP server leaks handler goroutines of hijacked connections.
	// There is *no way* to determine when those handlers are complete.
	// See https://github.com/golang/go/issues/57673
	closed bool
	mu     sync.RWMutex
}

func (l *Server) logf(format string, v ...interface{}) {
	if l.cfg.Logf == nil {
		return
	}
	l.cfg.Logf(format+"\n", v...)
}

func NewServer(cfg *ServerConfig) *Server {
	if cfg == nil {
		cfg = DefaultServerConfig
	}
	l := &Server{
		cfg:   cfg,
		monCh: make(chan string, 8),
		idle:  make(map[string]*Conn),

		connCh: make(chan *Conn, 8),
	}
	return l
}

func DefaultObservedAddr(r *http.Request) (addr netip.AddrPort, err error) {
	if addr, err = netip.ParseAddrPort(r.RemoteAddr); err != nil {
		return
	}
	if err = GoodObservedAddr(addr); err != nil {
		err = fmt.Errorf("%v: %w", addr, err)
	}
	return
}

func (l *Server) addObservedAddr(conn *Conn) {
	fn := l.cfg.ObservedAddrFunc
	if fn == nil {
		fn = DefaultObservedAddr
	}
	if observedAddr, err := fn(conn.req); err != nil {
		l.logf("ignore observed addr %v", err)
	} else {
		conn.meta.ObservedAddr = &observedAddr
	}
}

func (l *Server) AddClient(w http.ResponseWriter, req *http.Request) error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		http.Error(w, "rdv is closed", http.StatusServiceUnavailable)
		return ErrServerClosed
	}
	conn, err := upgradeRdv(w, req)
	if err != nil {
		return err
	}
	l.addObservedAddr(conn)
	l.connCh <- conn
	return nil
}

func (l *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	err := l.AddClient(w, r)
	if err != nil {
		l.logf("%v %v %v: %v", r.RemoteAddr, r.Method, r.URL.Path, err)
	}
}

// Closes the Server, unblocking concurrent accept calls.
// Adding to the Server after closing will panic.
func (l *Server) close() {
	l.mu.Lock()
	close(l.connCh) // panic intentionally if closed twice
	l.closed = true
	l.mu.Unlock()
}

func (l *Server) addIdle(conn *Conn) {
	l.idle[conn.meta.Token] = conn
	//l.wg.Add(1)
	go func() {
		//defer l.wg.Done()
		conn.SetDeadline(cfgDeadline(l.cfg.LobbyTimeout))
		n, err := conn.Read(make([]byte, 1))
		if !(n == 0 && errors.Is(err, os.ErrDeadlineExceeded)) {
			writeResponseErr(conn, http.StatusBadRequest, "conn must idle while waiting for response header")
		}
		l.monCh <- conn.meta.Token
	}()
}

// If there's an idle conn for the token, cancel it and await its monitoring, then return it
func (l *Server) interruptAndGetIdle(token string) *Conn {
	conn := l.idle[token]
	if conn == nil {
		return nil
	}
	// cancel the monitoring
	conn.SetDeadline(past())

	// wait for the monitoring to complete, which must happen very quickly
	for t := range l.monCh {
		// our conn's monitoring completed
		if t == token {
			break
		}
		// an unrelated conn's monitoring failed, kick it out until we get to ours
		l.kickOut(t)
	}
	delete(l.idle, token)
	return conn
}

// kick out of Server either from a timeout or breaking the protocol
func (l *Server) kickOut(token string) {
	conn := l.idle[token]
	delete(l.idle, token)
	// If there was a previous protocol error, this won't do anything because the conn is closed
	writeResponseErr(conn, http.StatusRequestTimeout, "no matching peer found")
	l.logf("left: %v", conn.meta.serverSummary())
}

func (l *Server) Serve() error {
	return l.ServeContext(context.Background())
}

// Runs the goroutines associated with the Server.
func (l *Server) ServeContext(ctx context.Context) error {
	wg := sync.WaitGroup{}
	defer wg.Wait()
	ctxCh := ctx.Done()
	for ctxCh != nil || l.connCh != nil || len(l.idle) > 0 {
		select {
		case <-ctxCh:
			ctxCh = nil
			l.close()

		//cancel() // send cancel signal to relay handlers
		case token := <-l.monCh:
			l.kickOut(token)
		case conn, ok := <-l.connCh:
			if !ok {
				l.logf("shutdown: %v idle conns", len(l.idle))
				l.connCh = nil // blocks forever, leaving monCh the only remaining channel
				//cancel()
				// no more conns, shutting down
				for _, ic := range l.idle {
					writeResponseErr(ic, http.StatusServiceUnavailable, "rdv server shutting down, try again")
				}
				continue
			}
			idleConn := l.interruptAndGetIdle(conn.meta.Token)
			// invariant: the idle conn is removed and no longer monitored
			if idleConn != nil && idleConn.meta.IsDialer != conn.meta.IsDialer {
				// happy path: the conn and idle conn are a match
				idleConn.SetDeadline(time.Time{})
				// Methods are unequal, we found a pair
				wg.Add(1)
				go func() {
					defer wg.Done()
					dc, ac := idleConn, conn
					if ac.meta.IsDialer {
						dc, ac = ac, dc // swap
					}
					l.cfg.ServeFunc(ctx, dc, ac)
				}()
				l.logf("matched: %v", conn.meta.Token)
				continue
			}
			// either there is no conn of the same token, or there's another of the same method
			l.addIdle(conn)
			// if conn is same method, kick the old one out
			if idleConn == nil {
				l.logf("joined: %v", conn.meta.serverSummary())
			} else {
				l.logf("replaced: %v", idleConn.meta.serverSummary())
				writeResponseErr(idleConn, http.StatusConflict, "replaced by another conn")
			}
		}
	}
	return ctx.Err()
}

// SetTimeout story
// Tap before or after write
//   - before: taps all data including what hasn't been sent, block writes, rate limit
//   - after: monitor bandwidth, measure exactly what was written
//
// Pair or conns
// - all else equal: conns (fewer types)
// - can taps be per conn? no because we're tapping the read
// Lobby and relay closure, graceful shutdown etc
// Close outside handler?
// - no risk of leakage
// - can't transfer to my own handler
// Handler or channel?
// - setting up the goroutines for relays is error-prone

func DefaultHandler(ctx context.Context, dc, ac *Conn) {

	// TODO: Should be able to reject requests, and write a response header
	DefaultRelayer.Run(ctx, dc, ac)
}
