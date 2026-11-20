package main

/*
#cgo pkg-config: gtk+-3.0 webkit2gtk-4.0
#include <webkit2/webkit2.h>

// webview_go adds its WebKitWebView as the GtkWindow's only child.
static void homeshell_set_zoom(void *window, double level) {
	GtkWidget *child = gtk_bin_get_child(GTK_BIN(window));
	if (WEBKIT_IS_WEB_VIEW(child)) {
		webkit_web_view_set_zoom_level(WEBKIT_WEB_VIEW(child), level);
	}
}
*/
import "C"

import "unsafe"

// setZoom applies level to the webview in window. Main thread only.
func setZoom(window unsafe.Pointer, level float64) {
	C.homeshell_set_zoom(window, C.double(level))
}
