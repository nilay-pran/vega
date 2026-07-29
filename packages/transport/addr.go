package transport

import (
	"fmt"
	"net"
	"net/url"
)

// SLKTAddr derives the host:port SLKT dials from the upload-server URL. The
// server multiplexes SLKT and HTTPS on one port, so both share the URL's
// authority; a missing port defaults to 443 to match the server default. Every
// client (daemon, CLI) points at a single -server URL and lets this split out
// the SLKT address.
func SLKTAddr(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("parse server url %q: %w", serverURL, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("server url %q has no host", serverURL)
	}
	if u.Port() == "" {
		return net.JoinHostPort(u.Hostname(), "443"), nil
	}
	return u.Host, nil
}
