package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// healthcheckTimeout bounds the whole probe; Docker's own timeout is longer.
const healthcheckTimeout = 3 * time.Second

// healthURL turns a listen address (KIPPLE_ADDR style: ":7080", "0.0.0.0:7080",
// "[::]:7080", "127.0.0.1:9090") into the loopback /healthz URL.
func healthURL(addr string) (string, error) {
	if addr == "" {
		addr = ":7080"
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

// runHealthcheck probes the local server: nil only on HTTP 200.
func runHealthcheck(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("healthcheck takes no arguments")
	}
	return probeHealth(os.Getenv("KIPPLE_ADDR"), healthcheckTimeout)
}

func probeHealth(addr string, timeout time.Duration) error {
	url, err := healthURL(addr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		return fmt.Errorf("unhealthy: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: %s returned %d", url, resp.StatusCode)
	}
	return nil
}
