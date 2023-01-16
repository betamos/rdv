package rdv

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func (m *Meta) toReq(ctx context.Context, header http.Header) (*http.Request, error) {

	method := "ACCEPT"
	if m.IsDialer {
		method = "DIAL"
	}
	req, err := http.NewRequestWithContext(ctx, method, m.ServerAddr, nil) // overwrite GET
	if err != nil {
		return nil, err
	}
	if header != nil {
		req.Header = header
	}
	req.Header.Set("Upgrade", protocolName)
	req.Header.Set("Connection", "upgrade")
	req.Header.Set(hToken, m.Token)
	req.Header.Set(hSelfAddrs, formatAddrs(m.SelfAddrs))
	return req, nil
}

func (m *Meta) toResp() *http.Response {
	resp := newUpgradeResponse(http.StatusSwitchingProtocols, protocolName)
	resp.Header.Set(hPeerAddrs, formatAddrs(m.PeerAddrs))
	if m.ObservedAddr != nil {
		resp.Header.Set(hObservedAddr, m.ObservedAddr.String()) // TODO: Rename header?
	}
	return resp
}

// Returns ErrUpgrade if upgrade is missing
func parseReq(req *http.Request) (m *Meta, err error) {
	m = new(Meta)
	if err := checkUpgradeRequest(req, protocolName); err != nil {
		return nil, err
	}
	m.IsDialer = req.Method == "DIAL"
	if !m.IsDialer && req.Method != "ACCEPT" {
		return nil, fmt.Errorf("%w: bad http method %v", ErrProtocol, req.Method)
	}
	m.Token = req.Header.Get(hToken)
	if m.Token == "" {
		return nil, fmt.Errorf("%w: missing token", ErrProtocol)
	}
	m.SelfAddrs, err = parseAddrs(req.Header.Get(hSelfAddrs))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid self addrs %s", ErrProtocol, req.Header.Get(hSelfAddrs))
	}
	if len(m.SelfAddrs) > maxAddrs-1 {
		return nil, fmt.Errorf("%w: too many self addrs %s", ErrProtocol, req.Header.Get(hSelfAddrs))
	}
	return m, nil
}

func (m *Meta) parseResp(resp *http.Response) (err error) {
	if err = checkUpgradeResponse(resp, protocolName); err != nil {
		return fmt.Errorf("%w: %v", ErrBadHandshake, err)
	}
	m.PeerAddrs, err = parseAddrs(resp.Header.Get(hPeerAddrs))
	if err != nil {
		return fmt.Errorf("%w: invalid peer addrs %s", ErrBadHandshake, resp.Header.Get(hPeerAddrs))
	}
	if len(m.PeerAddrs) > maxAddrs {
		return fmt.Errorf("%w: too many peer addrs %s", ErrBadHandshake, resp.Header.Get(hPeerAddrs))
	}

	if resp.Header.Get(hObservedAddr) != "" {
		observedAddr, err := netip.ParseAddrPort(resp.Header.Get(hObservedAddr))
		m.ObservedAddr = &observedAddr
		if err != nil {
			return fmt.Errorf("%w: invalid observed addr %s", ErrBadHandshake, resp.Header.Get(hObservedAddr))
		}
	}
	return nil
}

func dialRdvServer(ctx context.Context, socket *Socket, meta *Meta, reqHeader http.Header) (*Conn, *http.Response, error) {
	// Force ipv4 to allow for zero-stun
	req, err := meta.toReq(ctx, reqHeader)
	if err != nil {
		return nil, nil, err
	}
	nc, err := socket.DialURLContext(ctx, "tcp4", req.URL)
	if err != nil {
		return nil, nil, err
	}
	closers := []io.Closer{nc}
	defer closeAll(&closers)

	br := bufio.NewReader(nc)
	resp, err := doHttp(nc, br, req)
	if err != nil {
		return nil, nil, err
	}
	err = meta.parseResp(resp)
	if err != nil {
		slurp(resp, 1024)
		return nil, resp, err
	}
	closers = nil
	return newRelayConn(nc, br, meta, req), nil, nil
}

// Write a response err and close the conn, with a short deadline
func writeResponseErr(nc net.Conn, statusCode int, reason string) error {
	defer nc.Close()
	resp := newUpgradeResponse(statusCode, protocolName)
	resp.Body = io.NopCloser(strings.NewReader(reason))

	// From HTTP std lib
	resp.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp.Header.Set("X-Content-Type-Options", "nosniff")

	nc.SetDeadline(verySoon())
	return resp.Write(nc)
}

func upgradeRdv(w http.ResponseWriter, req *http.Request) (*Conn, error) {
	meta, err := parseReq(req)
	if errors.Is(err, ErrUpgrade) {
		http.Error(w, err.Error(), http.StatusUpgradeRequired)
		return nil, err
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, err
	}
	nc, brw, err := upgradeHttp(w, req, protocolName)
	if err != nil {
		return nil, err
	}
	if brw.Reader.Buffered() > 0 {
		err = fmt.Errorf("%w: received client data before response header", ErrProtocol)
		writeResponseErr(nc, http.StatusBadRequest, err.Error())
		return nil, err
	}

	sw := newRelayConn(nc, nc, meta, req)
	return sw, nil
}
