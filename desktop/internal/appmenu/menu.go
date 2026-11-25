// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

// Package appmenu gives the desktop shell a macOS main menu.
//
// The webview library creates no menu bar, and AppKit dispatches key
// equivalents such as Cmd+Q, Cmd+W and Cmd+C only through menu items, so
// without one those shortcuts do nothing. This file describes the menus as
// data, which is testable on any platform; menu_darwin.go builds them in
// AppKit, and Install is a no-op elsewhere.
package appmenu

// Modifier is a key-equivalent modifier mask. The values are AppKit's
// NSEventModifierFlags, so they pass to Objective-C unchanged.
type Modifier uint

const (
	Shift   Modifier = 1 << 17
	Control Modifier = 1 << 18
	Option  Modifier = 1 << 19
	Command Modifier = 1 << 20
)

// QuitAction is the selector of the Quit item. Unlike every other action it is
// not sent up the responder chain but to a target that calls Install's quit
// function, so quitting ends the webview's run loop instead of calling exit().
const QuitAction = "quit:"

// Item is one menu item. Action is an Objective-C selector; every action but
// QuitAction has a nil target, so AppKit sends it to the first responder that
// implements it (the WKWebView for the Edit items, the window or the
// application for the rest).
type Item struct {
	Title     string
	Action    string
	Key       string
	Modifiers Modifier
	Separator bool
}

// Menu is one top-level menu in the menu bar. Windows marks the menu AppKit
// maintains as NSApp's windowsMenu, listing the open windows.
type Menu struct {
	Title   string
	Items   []Item
	Windows bool
}

var separator = Item{Separator: true}

// Standard returns the menus a single-window macOS app is expected to have.
func Standard(appName string) []Menu {
	return []Menu{
		{Title: appName, Items: []Item{
			{Title: "About " + appName, Action: "orderFrontStandardAboutPanel:"},
			separator,
			{Title: "Hide " + appName, Action: "hide:", Key: "h", Modifiers: Command},
			{Title: "Hide Others", Action: "hideOtherApplications:", Key: "h", Modifiers: Command | Option},
			{Title: "Show All", Action: "unhideAllApplications:"},
			separator,
			{Title: "Quit " + appName, Action: QuitAction, Key: "q", Modifiers: Command},
		}},
		{Title: "Edit", Items: []Item{
			{Title: "Undo", Action: "undo:", Key: "z", Modifiers: Command},
			{Title: "Redo", Action: "redo:", Key: "z", Modifiers: Command | Shift},
			separator,
			{Title: "Cut", Action: "cut:", Key: "x", Modifiers: Command},
			{Title: "Copy", Action: "copy:", Key: "c", Modifiers: Command},
			{Title: "Paste", Action: "paste:", Key: "v", Modifiers: Command},
			{Title: "Select All", Action: "selectAll:", Key: "a", Modifiers: Command},
		}},
		{Title: "View", Items: []Item{
			{Title: "Enter Full Screen", Action: "toggleFullScreen:", Key: "f", Modifiers: Command | Control},
		}},
		{Title: "Window", Windows: true, Items: []Item{
			{Title: "Minimize", Action: "performMiniaturize:", Key: "m", Modifiers: Command},
			{Title: "Zoom", Action: "performZoom:"},
			separator,
			{Title: "Close Window", Action: "performClose:", Key: "w", Modifiers: Command},
		}},
	}
}
