package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/config"
)

// healthcheckTimeout bounds the whole probe; Docker's own timeout is longer.
const healthcheckTimeout = 3 * time.Second

// healthURL turns a listen address (KIPPLE_ADDR style: ":1919", "0.0.0.0:1919",
// "[::]:1919", "127.0.0.1:9090", "kipple-box:1919") into the /healthz URL: an
// empty or unspecified host becomes 127.0.0.1, any other host is kept (the
// server listens only there). An empty address is config.DefaultAddr.
func healthURL(addr string) (string, error) {
	if addr == "" {
		addr = config.DefaultAddr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("KIPPLE_ADDR %q: %w", addr, err)
	}
	if port == "" {
		return "", fmt.Errorf("KIPPLE_ADDR %q has no port", addr)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}

// healthAddrs is what the probe tries, in order. With KIPPLE_ADDR unset the
// server is on the default port or, when that was taken, the fallback port.
func healthAddrs(env string) []string {
	if env != "" {
		return []string{env}
	}
	return []string{config.DefaultAddr, config.FallbackAddr}
}

// runHealthcheck probes the local server: nil only on HTTP 200 "ok".
func runHealthcheck(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("healthcheck takes no arguments")
	}
	return probeHealthAny(healthAddrs(os.Getenv("KIPPLE_ADDR")), healthcheckTimeout)
}

// probeHealthAny probes each address in turn within one budget and succeeds on
// the first healthy one.
func probeHealthAny(addrs []string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var errs []error
	for _, a := range addrs {
		err := probeHealthCtx(ctx, a)
		if err == nil {
			return nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(errs...)
}

func probeHealth(addr string, timeout time.Duration) error {
	return probeHealthAny([]string{addr}, timeout)
}

func probeHealthCtx(ctx context.Context, addr string) error {
	req, err := healthRequest(ctx, addr)
	if err != nil {
		return err
	}
	url := req.URL.String()
	// A one-shot probe: no keep-alive, so no idle connection (and no read-loop
	// goroutine calling time.Now) outlives the call.
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	resp, err := (&http.Client{Transport: tr}).Do(req) // #nosec G704 -- the operator's own KIPPLE_ADDR, see healthRequest
	if err != nil {
		return fmt.Errorf("unhealthy: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: %s returned %d", url, resp.StatusCode)
	}
	// Another program on a probed port may answer 200 too: Kipple says "ok".
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16))
	if strings.TrimSpace(string(b)) != "ok" {
		return fmt.Errorf("unhealthy: %s did not answer like Kipple", url)
	}
	return nil
}

// healthRequest is the probe request for addr.
func healthRequest(ctx context.Context, addr string) (*http.Request, error) {
	url, err := healthURL(addr)
	if err != nil {
		return nil, err
	}
	// A probe of this process's own listener: the address is the operator's KIPPLE_ADDR (127.0.0.1 for an empty or
	// unspecified host, else the host it names, an IP or a name), never request input.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) // #nosec G704 -- the operator's own KIPPLE_ADDR, see above
	if err != nil {
		return nil, err
	}
	// A KIPPLE_ADDR that names a host (kipple-box:1919) is dialled by that name,
	// but the Host header says 127.0.0.1: in setup and open mode the Host gate
	// answers 421 to a name it does not allow, which would make a healthy server
	// report unhealthy. An IP literal always passes the gate.
	if h, port, err := net.SplitHostPort(req.URL.Host); err == nil && net.ParseIP(h) == nil {
		req.Host = net.JoinHostPort("127.0.0.1", port)
	}
	return req, nil
}
