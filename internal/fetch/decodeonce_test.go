package fetch

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// The fetcher decodes a body once: the decoded copy serves both the body-hash
// check and the parse.
func TestFetchDecodesBodyOnce(t *testing.T) {
	var n atomic.Int32
	orig := decodeBody
	decodeBody = func(b []byte, cs string) Decoded { n.Add(1); return orig(b, cs) }
	t.Cleanup(func() { decodeBody = orig })

	srv, c := feedServer(t, serveRSS)
	res := doFetch(t, c, snapFor(srv.URL+"/feed"))
	require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
	require.EqualValues(t, 1, n.Load())
	require.Equal(t, res.Feed.BodyHash, res.BodyHash)
}

func TestWhitespaceOnlyBodyIsEmpty(t *testing.T) {
	srv, c := feedServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(" \r\n\t ")) })
	res := doFetch(t, c, snapFor(srv.URL))
	require.Equal(t, ClassEmpty, res.ErrClass, res.ErrMsg)
}
