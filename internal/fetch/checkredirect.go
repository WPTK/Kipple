package fetch

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrTooManyHops is returned by CheckRedirect after more than 5 redirects.
var ErrTooManyHops = errors.New("stopped after 5 redirects")

// ErrRedirectRefused wraps the reason CheckRedirect will not follow a redirect.
var ErrRedirectRefused = errors.New("redirect refused")

// CheckRedirect is the redirect policy of every outbound HTTP client (feeds,
// discovery, full-text extraction, icons, the image proxy): at most 5 hops,
// http and https only, never from https down to http, and nothing the last hop
// said travels on: the user name and password of a Location, and the Referer.
// A client that holds its own credentials (a feed's HTTP auth) layers its own
// rule on top.
func CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxHops {
		return ErrTooManyHops
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return fmt.Errorf("%w: to a %q address", ErrRedirectRefused, req.URL.Scheme)
	}
	if prev := via[len(via)-1]; prev.URL.Scheme == "https" && req.URL.Scheme == "http" {
		return fmt.Errorf("%w: from https to http", ErrRedirectRefused)
	}
	// http.Client turns a Location's userinfo into an Authorization header when
	// it sends the hop, which happens after this check.
	req.URL.User = nil
	req.Header.Del("Referer")
	return nil
}
