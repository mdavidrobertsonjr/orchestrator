package hosted

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// Resolve and validate at dial time, then dial the validated IP, preventing DNS
// rebinding. Proxies are deliberately disabled: they bypass this check.
func publicHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if port != "80" && port != "443" {
			return nil, errors.New("only public HTTP and HTTPS destinations are supported")
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(addresses) == 0 {
			return nil, errors.New("destination has no addresses")
		}
		for _, ip := range addresses {
			if !publicIP(ip) {
				return nil, errors.New("private network destinations are not allowed")
			}
		}
		dialer := net.Dialer{Timeout: 10 * time.Second}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
	}
	return &http.Client{Transport: boundedTransport{transport}, Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" && req.URL.Scheme != "http" {
			return errors.New("unsupported redirect")
		}
		return nil
	}}
}
func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16", "64:ff9b::/96", "64:ff9b:1::/48"} {
		if netip.MustParsePrefix(cidr).Contains(ip) {
			return false
		}
	}
	return true
}

type boundedTransport struct{ next http.RoundTripper }

func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	// Some public ATS boards return large but bounded JSON documents. Keep a
	// hard memory limit while allowing the largest known Ashby boards through.
	response.Body = http.MaxBytesReader(nil, response.Body, 16<<20)
	return response, nil
}
