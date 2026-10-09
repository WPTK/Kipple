package fetch

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
)

// Error classes (fetch_log.error_class, design §2.2).
const (
	ClassTimeout      = "timeout"
	ClassDNS          = "dns"
	ClassConnect      = "connect"
	ClassTLS          = "tls"
	ClassHTTP         = "http"
	ClassCloudflare   = "cloudflare"
	ClassTooLarge     = "too_large"
	ClassEmpty        = "empty"
	ClassParse        = "parse"
	ClassSSRF         = "ssrf"
	ClassRedirectLoop = "redirect_loop"
	ClassGone         = "gone"
)

// Classify maps a transport-level error to an error class and message
// (design §4.5).
func Classify(err error) (class, msg string) {
	var blocked *BlockedError
	var dnsErr *net.DNSError
	var maxBytes *http.MaxBytesError
	var unknownCA x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var certInvalid x509.CertificateInvalidError
	var verifyErr *tls.CertificateVerificationError
	var recHdr tls.RecordHeaderError
	var opErr *net.OpError
	var urlErr *url.Error

	switch {
	case errors.As(err, &blocked):
		return ClassSSRF, blocked.Error()
	case errors.Is(err, ErrTooManyHops):
		return ClassRedirectLoop, ErrTooManyHops.Error()
	case errors.Is(err, errRedirectDowngrade):
		return ClassRedirectLoop, "the feed redirects from https to plain http, which Kipple does not follow; if you trust that site, edit the feed to its http address"
	case errors.Is(err, ErrRedirectRefused):
		return ClassRedirectLoop, "the address redirects to something other than a web address, which Kipple does not follow"
	case errors.As(err, &maxBytes):
		return ClassTooLarge, tooLargeMessage(maxBytes.Limit)
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		return ClassTimeout, "timed out"
	case errors.As(err, &dnsErr):
		return ClassDNS, dnsErr.Error()
	case errors.As(err, &unknownCA), errors.As(err, &hostErr), errors.As(err, &certInvalid),
		errors.As(err, &verifyErr), errors.As(err, &recHdr):
		return ClassTLS, err.Error()
	case errors.As(err, &opErr), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF), errors.As(err, &urlErr):
		return ClassConnect, err.Error()
	}
	return ClassConnect, err.Error()
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// tooLargeMessage words the too_large error with the limit actually enforced
// (Options.MaxResponseBody), in MiB when it is a whole number of them.
func tooLargeMessage(limit int64) string {
	switch {
	case limit <= 0:
		return "response too large"
	case limit%(1<<20) == 0:
		return fmt.Sprintf("response larger than %d MiB", limit>>20)
	case limit%(1<<10) == 0:
		return fmt.Sprintf("response larger than %d KiB", limit>>10)
	}
	return fmt.Sprintf("response larger than %d bytes", limit)
}
