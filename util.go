package main

import "path/filepath"

// parentDir returns the directory portion of a file path.
func parentDir(p string) string {
	return filepath.Dir(p)
}
