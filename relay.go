package rdv

import (
	"context"
	"io"
	"time"

	"golang.org/x/sync/errgroup"
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
	err := writeResponseErr(dc, statusCode, reason)
	err2 := writeResponseErr(ac, statusCode, reason)
	if err != nil {
		return err
	}
	return err2
}

func (r *Relayer) Run(ctx context.Context, dc, ac *Conn) (dn int64, ln int64, err error) {

	// TODO(go-1.20): Replace with cancellation cause
	g, ctx := errgroup.WithContext(ctx)

	it := newIdleTimer(ctx, r.IdleTimeout) // use group context to cancel properly
	g.Go(it.Wait)                          // idle timeout
	g.Go(func() error {
		err := initiateRelay(ac, dc) // Rock on!
		if err != nil {
			return err
		}
		it.Extend()
		dn, err = copyRelay(ac, dc, r.DialTap, it)
		return err
	})
	g.Go(func() error {
		err := initiateRelay(dc, ac)
		if err != nil {
			return err
		}
		ln, err = copyRelay(dc, ac, r.AcceptTap, it)
		return err
	})
	<-ctx.Done()
	dc.Close()
	ac.Close()
	err = g.Wait()
	if err == io.EOF {
		err = nil
	}
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
func copyRelay(to io.Writer, from io.Reader, tap io.Writer, it *idleTimer) (n int64, err error) {
	var w io.Writer = it
	if tap != nil {
		w = io.MultiWriter(it, tap)
	}
	n, err = io.Copy(to, io.TeeReader(from, w))
	if err == nil {
		err = io.EOF
	}
	return
}
