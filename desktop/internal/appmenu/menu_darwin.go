// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build darwin

package appmenu

/*
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "menu_darwin.h"
*/
import "C"

import (
	"strconv"
	"strings"
	"unsafe"
)

var quitHandler func()

//export appmenuQuit
func appmenuQuit() {
	if quitHandler != nil {
		quitHandler()
	}
}

// Install replaces the application's menu bar with Standard(appName), with
// Quit calling quit. It must run on the main thread once NSApp exists, which
// webview.New guarantees.
func Install(appName string, quit func()) {
	quitHandler = quit
	C.appmenuBegin()
	for _, menu := range Standard(appName) {
		title := C.CString(menu.Title)
		C.appmenuAddMenu(title, C.bool(menu.Windows))
		C.free(unsafe.Pointer(title))
		for _, item := range menu.Items {
			addItem(item)
		}
	}
	C.appmenuInstall()
}

func addItem(item Item) {
	if item.Separator {
		C.appmenuAddSeparator()
		return
	}
	title := C.CString(item.Title)
	action := C.CString(item.Action)
	key := C.CString(item.Key)
	defer func() {
		C.free(unsafe.Pointer(title))
		C.free(unsafe.Pointer(action))
		C.free(unsafe.Pointer(key))
	}()
	C.appmenuAddItem(title, action, key, C.ulong(item.Modifiers))
}

// installedMenus reads the menu bar back out of AppKit.
func installedMenus() []Menu {
	description := C.appmenuDescribe()
	defer C.free(unsafe.Pointer(description))
	return parseDescription(C.GoString(description))
}

func parseDescription(description string) []Menu {
	var menus []Menu
	for _, line := range strings.Split(strings.TrimSuffix(description, "\n"), "\n") {
		fields := strings.Split(line, "\t")
		switch fields[0] {
		case "menu":
			menus = append(menus, Menu{Title: fields[1], Windows: fields[2] == "1"})
		case "separator":
			current := &menus[len(menus)-1]
			current.Items = append(current.Items, separator)
		case "item":
			modifiers, _ := strconv.ParseUint(fields[4], 10, 64)
			current := &menus[len(menus)-1]
			current.Items = append(current.Items, Item{
				Title: fields[1], Action: fields[2], Key: fields[3], Modifiers: Modifier(modifiers),
			})
		}
	}
	return menus
}

// performKeyEquivalent sends a synthesized key-down through the menu bar and
// reports whether an item handled it.
func performKeyEquivalent(key string, modifiers Modifier) bool {
	characters := C.CString(key)
	defer C.free(unsafe.Pointer(characters))
	return bool(C.appmenuPerformKeyEquivalent(characters, C.ulong(modifiers)))
}

// dispatchQuitAppleEvent sends a quit Apple event through the Apple event
// manager and reports whether a handler accepted it.
func dispatchQuitAppleEvent() bool {
	return bool(C.appmenuDispatchQuitAppleEvent())
}
