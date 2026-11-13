package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

// webview_go makes its WKWebView the NSWindow's content view. pageZoom needs
// macOS 11; older systems simply do not zoom.
static void homeshell_set_zoom(void *window, double level) {
	NSView *view = [(NSWindow *)window contentView];
	if (![view isKindOfClass:[WKWebView class]]) {
		return;
	}
	if (@available(macOS 11.0, *)) {
		[(WKWebView *)view setPageZoom:level];
	}
}
*/
import "C"

import "unsafe"

// setZoom applies level to the webview in window. Main thread only.
func setZoom(window unsafe.Pointer, level float64) {
	C.homeshell_set_zoom(window, C.double(level))
}
