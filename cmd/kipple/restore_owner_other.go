//go:build !unix

package main

import "io"

// fixOwnership: file ownership is a Unix concern.
func fixOwnership(io.Writer, string, ...string) {}
