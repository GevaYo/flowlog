//go:build !darwin && !linux

package main

import "os"

// termWidth is unavailable on this platform; wrapping is disabled.
func termWidth(f *os.File) int { return 0 }
