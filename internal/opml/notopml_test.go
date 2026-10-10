package opml

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRefusesWellFormedNonOPML(t *testing.T) {
	for _, s := range []string{`<rss version="2.0"><channel/></rss>`, `<html><body/></html>`, `<feed xmlns="http://www.w3.org/2005/Atom"/>`} {
		_, err := Parse(strings.NewReader(s))
		require.ErrorIs(t, err, ErrNotOPML, s)
	}
	_, err := Parse(strings.NewReader(`<OPML version="2.0"><body/></OPML>`))
	require.NoError(t, err)
}
