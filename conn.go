package rdv

import (
	"fmt"
	"io"
	"net"
	"net/http"
)

type Conn struct {
	net.Conn
	r       io.Reader // TODO: Always bufio.Reader?
	isRelay bool
	meta    *Meta
	req     *http.Request
}

func newDirectConn(nc net.Conn, meta *Meta, req *http.Request) *Conn {
	return &Conn{
		Conn:    nc,
		r:       nc,
		isRelay: false,
		meta:    meta,
		req:     req,
	}
}

func newRelayConn(nc net.Conn, r io.Reader, meta *Meta, req *http.Request) *Conn {
	return &Conn{
		Conn:    nc,
		r:       r,
		isRelay: true,
		meta:    meta,
		req:     req,
	}
}

func (c *Conn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

func (c *Conn) Meta() *Meta {
	return c.meta
}

// Returns the http request for this conn. Read-only, so don't use its context or body.
func (c *Conn) Request() *http.Request {
	return c.req
}

func (c *Conn) IsRelay() bool {
	return c.isRelay
}

// Returns the rdv header, e.g. "rdv/1 HELLO token" + CRLF
func rdvHeader(method, token string) string {
	return fmt.Sprintf("%s %s %s\r\n", protocolName, method, token)
}

// The rdv header lines that should be sent by this peer and received by the other peer,
// upon successful connection.
func (c *Conn) headers() (self string, peer string) {
	ah := rdvHeader("HELLO", c.meta.Token)
	dh := rdvHeader("CONFIRM", c.meta.Token)
	if c.meta.IsDialer {
		return dh, ah
	}
	return ah, dh
}

// Establishes candidate connections. Dialers simply read hello, whereas acceptors write hello
// and read confirm. Invoked multiple times, but succeeds at most once for acceptors.
func (c *Conn) clientHand() error {
	self, peer := c.headers()
	if c.meta.IsDialer {
		return expectStr(c, peer)
	}
	_, err := io.WriteString(c, self)
	if err != nil {
		return err
	}
	return expectStr(c, peer)
}

// Finalizes candidate selection. Dialers write the confirm, whereas the listener do nothing
// (they already read the confirm earlier). Invoked at most once, IFF clientHand succeeded.
func (c *Conn) clientShake() error {
	if c.meta.IsDialer {
		self, _ := c.headers()
		_, err := io.WriteString(c, self)
		return err
	}
	return nil
}
