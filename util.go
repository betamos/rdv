package rdv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

func urlPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch u.Scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}

func parseAddrs(addrStr string) (addrs []netip.AddrPort, err error) {
	if addrStr == "" {
		return nil, nil
	}
	for _, part := range splitAndTrim(addrStr, ",") {
		addr, err := netip.ParseAddrPort(part)
		if err != nil {
			return nil, err
		}
		addrs = append(addrs, addr)
	}
	return
}

func strSliceContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func splitAndTrim(s, sep string) (parts []string) {
	for _, part := range strings.Split(s, sep) {
		parts = append(parts, strings.TrimSpace(part))
	}
	return
}

func formatAddrs(addrs []netip.AddrPort) string {
	var parts []string
	for _, addr := range addrs {
		parts = append(parts, addr.String())
	}
	return strings.Join(parts, ", ")
}

func ctxIO(ctx context.Context, nc net.Conn) (resetFn func()) {

	done := make(chan struct{})
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		defer close(done)
		<-ctx.Done()
		nc.SetDeadline(past())
	}()
	return func() {
		cancel()
		<-done
		nc.SetDeadline(time.Time{})
	}
}

func closeAll(closers *[]io.Closer) {
	for _, closer := range *closers {
		closer.Close()
	}
}

func cfgDeadline(d time.Duration) (t time.Time) {
	if d > 0 {
		t = time.Now().Add(d)
	}
	return
}

func past() time.Time {
	return time.Now().Add(-time.Second)
}

func verySoon() time.Time {
	return time.Now().Add(10 * time.Millisecond)
}

func expectStr(r io.Reader, str string) error {
	expected := []byte(str)
	actual := make([]byte, len(expected))
	_, err := io.ReadFull(r, actual)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("%v: invalid peer handshake", ErrProtocol)
	}
	return nil
}

type idleTimer struct {
	ctx     context.Context
	timeout time.Duration
	dirty   uint32 // atomic bool
}

func newIdleTimer(ctx context.Context, timeout time.Duration) *idleTimer {
	return &idleTimer{ctx, timeout, 0}
}

// Registers activity and prolongs the deadline
func (t *idleTimer) Extend() {
	atomic.StoreUint32(&t.dirty, 1)
}

// Resets the dirty state and returns whether it was extended
func (t *idleTimer) reset() (extended bool) {
	return atomic.SwapUint32(&t.dirty, 0) != 0
}

func (t *idleTimer) Write(p []byte) (int, error) {
	t.Extend()
	return len(p), nil
}

var ErrIdleTimeout = errors.New("periodic idle timeout exceeded")

// Cleans up resources
func (t *idleTimer) Wait() error {
	var tickCh <-chan time.Time // Nil-channel blocks forever
	if t.timeout > 0 {
		ticker := time.NewTicker(t.timeout)
		tickCh = ticker.C
		defer ticker.Stop()
	}
	for {
		select {
		case <-tickCh:
			if !t.reset() { // new, old
				return ErrIdleTimeout
			}
		case <-t.ctx.Done():
			return t.ctx.Err()
		}
	}
}

// Unwraps any net.OpError to prevent address noise
func unwrapOp(err error) error {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Err
	}
	return err
}

// Filters and returns a new slice where fn returns true
func filter[T any](ts []T, fn func(t T) bool) (ret []T) {
	for _, t := range ts {
		if fn(t) {
			ret = append(ret, t)
		}
	}
	return
}
