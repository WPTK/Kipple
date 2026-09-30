//go:build !windows

package setup

// restrictToOwner is a no-op outside Windows: the 0600 mode the token file is
// created with already makes it owner-only.
func restrictToOwner(string) error { return nil }
