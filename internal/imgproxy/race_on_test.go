//go:build race

package imgproxy

// raceEnabled is true when the tests run under the race detector (CI), which
// changes allocation and timing: memory assertions are skipped there.
const raceEnabled = true
