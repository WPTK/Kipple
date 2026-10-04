//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// rss returns the resident size (VmRSS) and its peak (VmHWM) of a process, in bytes (Linux /proc).
func rss(pid int) (cur, peak uint64, err error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(v)
		if len(fields) == 0 {
			continue
		}
		n, _ := strconv.ParseUint(fields[0], 10, 64)
		switch k {
		case "VmRSS":
			cur = n * 1024
		case "VmHWM":
			peak = n * 1024
		}
	}
	return cur, peak, sc.Err()
}
