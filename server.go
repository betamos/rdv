package rdv

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"sync"
	"time"
)

type ServerConfig struct {
	// Amount of time that on peer can wait in the lobby for its partner. Zero means no timeout.
	LobbyTimeout time.Duration

	// Function to serve a relay connection between dialer and server.
	// The provided context is canceled along with the server.
	// The function is responsible for closing the connections.
	// Used to customize monitoring, rate limiting, idle timeouts relating to relay
	// connections. See RelayConfig for defaults.
	ServeFunc func(ctx context.Context, dc, ac *Conn)

	// Determines the remote addr:port from the client request, and adds it to the set of
	// candidate addrs sent to the other peer. If nil, `req.RemoteAddr` is used.
	// If your server is behind a load balancer, reverse proxy or similar, you may need to extract
	// the address using forwarding headers. To disable this feature, return an error.
	// See the server setup guide for details.
	ObservedAddrFunc func(req *http.Request) (netip.AddrPort, error)

	// Logging function.
	Logger *slog.Logger
}

func (c *ServerConfig) setDefaults() {
	if c.ServeFunc == nil {
		c.ServeFunc = DefaultHandler
	}
	if c.ObservedAddrFunc == nil {
		c.ObservedAddrFunc = DefaultObservedAddr
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

type Server struct {
	cfg    ServerConfig
	idle   map[string]*Conn
	connCh chan *Conn // Incoming upgraded conns: request received, no response sent, no deadline

	monCh chan string // token sent when current conn mapping is complete

	// Guards connCh because Go's HTTP server leaks handler goroutines of hijacked connections.
	// There is *no way* to determine when those handlers are complete.
	// See https://github.com/golang/go/issues/57673
	closed bool
	mu     sync.RWMutex
}

func NewServer(cfg *ServerConfig) *Server {
	s := &Server{
		monCh: make(chan string, 8),
		idle:  make(map[string]*Conn),

		connCh: make(chan *Conn, 8),
	}

	if cfg != nil {
		s.cfg = *cfg
	}
	s.cfg.setDefaults()
	return s
}

func DefaultObservedAddr(r *http.Request) (netip.AddrPort, error) {
	return netip.ParseAddrPort(r.RemoteAddr)
}

func (l *Server) addObservedAddr(conn *Conn) {
	if observedAddr, err := l.cfg.ObservedAddrFunc(conn.req); err != nil {
		l.cfg.Logger.Warn("rdv server: could not get observed addr", "err", err)
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
		l.cfg.Logger.Info("rdv server: bad request", "request", r.URL, "err", err)
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
	conn.SetDeadline(cfgDeadline(l.cfg.LobbyTimeout))
	//l.wg.Add(1)
	go func() {
		//defer l.wg.Done()
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
	l.cfg.Logger.Debug("rdv server: client timed out", "token", conn.meta.Token, "addr", conn.meta.ObservedAddr)
}

// Runs the goroutines associated with the Server.
func (l *Server) Serve(ctx context.Context) error {
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
				l.cfg.Logger.Info("rdv server: shutting down", "lobby_conns", len(l.idle))
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
				dc, ac := idleConn, conn
				if ac.meta.IsDialer {
					dc, ac = ac, dc // swap
				}
				wg.Add(1)
				go func(dc, ac *Conn) {
					defer wg.Done()
					l.cfg.ServeFunc(ctx, dc, ac)
				}(dc, ac)
				l.cfg.Logger.Info("rdv server: matched", "token", conn.meta.Token, "dial_addr", dc.meta.ObservedAddr, "accept_addr", ac.meta.ObservedAddr)
				continue
			}
			// either there is no conn of the same token, or there's another of the same method
			l.addIdle(conn)
			// if conn is same method, kick the old one out
			if idleConn == nil {
				l.cfg.Logger.Debug("rdv server: joined", "token", conn.meta.Token, "addr", conn.meta.ObservedAddr)
			} else {
				l.cfg.Logger.Debug("rdv server: replaced", "client", conn.meta.Token, "addr", conn.meta.ObservedAddr)
				writeResponseErr(idleConn, http.StatusConflict, "replaced by another conn")
			}
		}
	}
	return ctx.Err()
}

func DefaultHandler(ctx context.Context, dc, ac *Conn) {
	DefaultRelayer.Run(ctx, dc, ac)
}
