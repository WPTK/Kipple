// Package web embeds the built frontend (web/dist, produced by
// `npm run build`) into the Kipple binary.
//
// web/dist is gitignored except for a committed web/dist/.gitkeep, so that
// `go build`/`go test` succeed on a fresh clone before the frontend has ever
// been built: go:embed requires its pattern to match at least one file, and
// the "all:" prefix is what makes it include a dot-prefixed file like
// .gitkeep. HasIndex reports whether a real build (with an index.html) is
// present; internal/web falls back to a placeholder page when it is not.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
