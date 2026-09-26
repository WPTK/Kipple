//go:build race

package store

// raceEnabled is true when the tests run under the race detector (CI), where timing ceilings stretch.
const raceEnabled = true
