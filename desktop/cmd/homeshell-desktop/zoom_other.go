//go:build !linux && !darwin

package main

import "unsafe"

// setZoom is a no-op where HomeShell has no supported system webview.
func setZoom(window unsafe.Pointer, level float64) {}
