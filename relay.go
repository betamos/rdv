package rdv

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

type Relayer struct {
	DialTap, AcceptTap io.Writer

	// At least this much inactivity is allowed on both peers before terminating the connection.
	// Recommended at least 30s to account for network conditions and
	// application level heartbeats. Zero means no timeout.
	// As relays may serve a lot of traffic, activity is checked at an interval.
	IdleTimeout time.Duration
}

var DefaultRelayer = &Relayer{
	IdleTimeout: time.Minute,
}

func (r *Relayer) Reject(dc, ac *Conn, statusCode int, reason string) error {
	return errors.Join(
		writeResponseErr(dc, statusCode, reason),
		writeResponseErr(ac, statusCode, reason))
}

// Runs the relay service. Return actual data transferred and the first error that occurred.
// In case one end closed the connection in a normal manner, the error is io.EOF.
func (r *Relayer) Run(ctx context.Context, dc, ac *Conn) (dn int64, an int64, err error) {

	ctx, cancel := context.WithCancelCause(ctx)

	// Causes all IO to return timeout errors immediately
	timeoutFn := sync.OnceFunc(func() {
		dc.SetDeadline(past())
		ac.SetDeadline(past())
	})
	stop := context.AfterFunc(ctx, timeoutFn)
	defer stop()

	it := newIdleTimer(r.IdleTimeout, timeoutFn)
	defer it.Stop()
	dTap, aTap := r.taps()

	// Start only one extra goroutine to save resources
	go func() {
		dn = copyRelay(ac, dc, dTap, it, cancel)
	}()
	an = copyRelay(dc, ac, aTap, it, cancel)
	err = context.Cause(ctx)
	return
}

func copyRelay(to, from *Conn, tap io.Writer, it *idleTimer, cancel context.CancelCauseFunc) (n int64) {
	defer to.Close()
	err := initiateRelay(to, from)
	if err != nil {
		return
	}
	n, err = copyRelayInner(to, from, tap, it)
	cancel(err)
	return
}

// Sends response header containing addresses from the other conn,
// reads the rdv header line and relays it. Returns EOF if the rdv header line
// wasn't received, which typically indicates that p2p was established out-of-bounds.
func initiateRelay(to, from *Conn) error {

	to.meta.setPeerAddrsFrom(from.meta)
	resp := to.meta.toResp()
	err := resp.Write(to)
	if err != nil {
		return err
	}

	// Read expected rdv header line
	selfHeader, _ := from.headers()
	err = expectStr(from, selfHeader)
	if err != nil {
		return err
	}
	// Write rdv header line to the other peer
	_, err = io.WriteString(to, selfHeader)
	return err
}

// Copies data with the configured tap
func copyRelayInner(to io.WriteCloser, from io.Reader, tap io.Writer, it *idleTimer) (n int64, err error) {
	w := io.MultiWriter(it, tap, to)
	n, err = io.Copy(w, from)
	if err == nil {
		err = io.EOF
	}
	return
}

// Utility to get non-nil taps
func (r *Relayer) taps() (dTap, aTap io.Writer) {
	dTap, aTap = r.DialTap, r.AcceptTap
	if dTap == nil {
		dTap = noopTap{}
	}
	if aTap == nil {
		aTap = noopTap{}
	}
	return
}

type noopTap struct{}

func (noopTap) Write(p []byte) (n int, err error) {
	return len(p), nil
}
