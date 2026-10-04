// Command scalegen builds a large synthetic Kipple library and measures a Kipple build against it.
// See docs/performance.md.
//
//	go run ./scripts/scalegen gen   -dir DIR            write DIR/kipple.db (500 feeds, 150k items)
//	go run ./scripts/scalegen feeds -dir DIR            serve the feeds of that database for a refresh
//	go run ./scripts/scalegen rewind -db F -schema N   turn a copy into an older schema (6, 10 or 11)
//	go run ./scripts/scalegen bench -bin kipple.exe ... run the whole measurement
package main

import (
	"fmt"
	"os"

	_ "modernc.org/sqlite" // the driver the store package registers; the generator also opens files directly
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: scalegen gen|feeds|rewind|bench [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "gen":
		err = runGen(os.Args[2:])
	case "feeds":
		err = runFeeds(os.Args[2:])
	case "rewind":
		err = runRewind(os.Args[2:])
	case "bench":
		err = runBench(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "scalegen:", err)
		os.Exit(1)
	}
}
