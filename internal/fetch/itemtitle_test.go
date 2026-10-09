package fetch

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestItemTitlesAreStoredAsPlainText(t *testing.T) {
	const doc = `<?xml version="1.0"?><rss version="2.0"><channel><title>F</title>
<item><guid>1</guid><title><![CDATA[<b>Bold</b> &amp; <img src=x onerror=alert(1)>plain]]></title></item>
<item><guid>2</guid><title>&lt;script&gt;alert(1)&lt;/script&gt;Hello</title></item>
<item><guid>3</guid><title>5 &lt; 6 and 7 &gt; 3, Tom &amp; Jerry</title></item>
<item><guid>4</guid><title>  Café  ☕ </title></item>
</channel></rss>`
	f, err := ParseFeed([]byte(doc), ParseOptions{FeedURL: "https://e.example/feed", Content: func(raw string, _ ...string) (string, string) { return raw, raw }})
	require.NoError(t, err)
	var got []string
	for _, it := range f.Items {
		got = append(got, it.Title)
	}
	require.Equal(t, []string{"Bold & plain", "alert(1)Hello", "5 < 6 and 7 > 3, Tom & Jerry", "Café  ☕"}, got)
}
