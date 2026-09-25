package sched

import "time"

// Run is an in-memory batch of jobs the UI can follow (design §4.1). It is
// owned by the dispatcher.
type Run struct {
	ID          int64
	Kind        string
	Total       int
	Done        int
	NewItems    int
	Errors      int
	Outstanding int

	lastProgress time.Time
}
