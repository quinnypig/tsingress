package proxy

import (
	"context"
	"net"
	"net/http"
	"time"
)

// Dialer is a function that dials through the tailnet.
type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

// NewTailnetTransport returns an http.RoundTripper that dials backends through tsnet.
func NewTailnetTransport(dial Dialer) http.RoundTripper {
	return &http.Transport{
		DialContext:           dial,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:  10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
}
