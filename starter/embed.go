// Package starter is the recommended-feeds list the setup wizard offers
// (docs/setup-wizard-design.md section 7). feeds.json is the one source of
// truth: the server parses it once and subscribes only the ids it lists.
package starter

import (
	_ "embed"
	"sync"
)

//go:embed feeds.json
var feedsJSON []byte

var (
	loadOnce sync.Once
	loaded   *File
	loadErr  error
)

// Load parses and validates the embedded list once. A file that fails (which
// the package test prevents from being committed) is an error the caller shows
// as "no recommendations available", never a failed start.
func Load() (*File, error) {
	loadOnce.Do(func() { loaded, loadErr = Parse(feedsJSON) })
	return loaded, loadErr
}
