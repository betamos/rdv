package rdv

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func doHttp(nc net.Conn, br *bufio.Reader, req *http.Request) (*http.Response, error) {
	reset := ctxIO(req.Context(), nc)
	defer reset()
	err := req.Write(nc)
	if err != nil {
		return nil, err
	}
	return http.ReadResponse(br, nil)
}

// Use checkUpgradeResponse or checkUpgradeRequest instead
func checkUpgrade(h http.Header, proto string, single bool) error {
	connection := strings.ToLower(h.Get("Connection"))
	if connection != "upgrade" {
		return fmt.Errorf("%w: requires connection upgrade", ErrUpgrade)
	}
	upgrade := strings.ToLower(h.Get("Upgrade"))
	if upgrade == "" {
		return fmt.Errorf("%w: missing upgrade header", ErrUpgrade)
	}
	protos := splitAndTrim(upgrade, ",")
	if !strSliceContains(protos, proto) || (single && len(protos) != 1) {
		return fmt.Errorf("%w: bad upgrade %s", ErrUpgrade, upgrade)
	}
	return nil
}

func checkUpgradeResponse(resp *http.Response, protocol string) error {
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("unexpected http status %v", resp.Status)
	}
	return checkUpgrade(resp.Header, protocol, true)
}

func checkUpgradeRequest(r *http.Request, protocol string) error {
	// Check that upgrade is intended before protocol, to report a better error
	if err := checkUpgrade(r.Header, protocol, true); err != nil {
		return err
	}
	if strings.ToLower(r.Proto) != "http/1.1" {
		return fmt.Errorf("%w: bad http version for upgrade %s", ErrUpgrade, r.Proto)
	}
	return nil
}

// Slurp up a bit of the response body to aid in debugging prior to closing the response.
func slurp(resp *http.Response, size int) {
	buf := make([]byte, size)
	n, _ := io.ReadFull(resp.Body, buf)
	resp.Body = io.NopCloser(bytes.NewReader(buf[:n]))
}

func newUpgradeResponse(statusCode int, protocol string) *http.Response {
	resp := &http.Response{
		ProtoMajor: 1,
		ProtoMinor: 1,
	}
	resp.StatusCode = statusCode
	h := make(http.Header)
	h.Set("Connection", "Upgrade")
	h.Set("Upgrade", protocol)
	resp.Header = h
	return resp
}

func upgradeHttp(w http.ResponseWriter, req *http.Request, protocol string) (net.Conn, *bufio.ReadWriter, error) {
	w.Header().Set("Connection", "upgrade")
	w.Header().Set("Upgrade", protocol)
	h, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "", http.StatusInternalServerError)
		return nil, nil, ErrHijackFailed
	}

	nc, brw, err := h.Hijack()
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return nil, nil, fmt.Errorf("%w: %v", ErrHijackFailed, err)
	}
	req.Body = nil
	nc.SetDeadline(time.Time{})
	return nc, brw, err
}
