//go:build !windows

package main

func nativeProbe(raw []byte) bool { return false }
