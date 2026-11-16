//go:build !linux

package service

// deviceOf only means something on the box; this keeps the package compiling in an editor.
func deviceOf(string) (uint64, bool) { return 0, false }
