package ftrun

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/extract"
)

// The extraction learns the feed's own host, the only host its network
// exceptions (allow_private_net, allow_insecure_tls) may be used for.
func TestExtractionGetsFeedHost(t *testing.T) {
	e := newEnv(t) // feed https://feed.test/f with allow_private_net
	var mu sync.Mutex
	var got []extract.Target
	fx := &fakeExt{fn: func(tg extract.Target) (extract.Result, error) {
		mu.Lock()
		got = append(got, tg)
		mu.Unlock()
		return extract.Result{HTML: "<p>text</p>", Text: "text", WordCount: 1, SourceURL: tg.URL}, nil
	}}
	r := New(Options{DB: e.db, Extractor: fx, Log: quiet()})

	_, err := r.Run(context.Background(), Request{Item: e.item("https://art.test/1"), Now: 100, Timeout: 5 * time.Second})
	require.NoError(t, err)
	e.exec("UPDATE feeds SET allow_private_net = 0, allow_insecure_tls = 1 WHERE id = ?", e.fd)
	_, err = r.Run(context.Background(), Request{Item: e.item("https://art.test/2"), Now: 100, Timeout: 5 * time.Second})
	require.NoError(t, err)
	e.exec("UPDATE feeds SET allow_insecure_tls = 0 WHERE id = ?", e.fd)
	_, err = r.Run(context.Background(), Request{Item: e.item("https://art.test/3"), Now: 100, Timeout: 5 * time.Second})
	require.NoError(t, err)

	require.Len(t, got, 3)
	require.True(t, got[0].AllowPrivate)
	require.Equal(t, "feed.test", got[0].FeedHost)
	require.True(t, got[1].InsecureTLS)
	require.Equal(t, "feed.test", got[1].FeedHost)
	require.Equal(t, "", got[2].FeedHost, "no exception: no lookup")
}
