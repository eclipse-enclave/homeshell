// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package appmenu

import (
	"testing"
)

func findItem(menus []Menu, menuTitle, itemTitle string) (Item, bool) {
	for _, menu := range menus {
		if menu.Title != menuTitle {
			continue
		}
		for _, item := range menu.Items {
			if item.Title == itemTitle {
				return item, true
			}
		}
	}
	return Item{}, false
}

func TestStandardBindsTheExpectedShortcuts(t *testing.T) {
	tests := []struct {
		menu      string
		item      string
		action    string
		key       string
		modifiers Modifier
	}{
		{menu: "HomeShell", item: "Quit HomeShell", action: QuitAction, key: "q", modifiers: Command},
		{menu: "HomeShell", item: "Hide HomeShell", action: "hide:", key: "h", modifiers: Command},
		{menu: "HomeShell", item: "Hide Others", action: "hideOtherApplications:", key: "h",
			modifiers: Command | Option},
		{menu: "Edit", item: "Undo", action: "undo:", key: "z", modifiers: Command},
		{menu: "Edit", item: "Redo", action: "redo:", key: "z", modifiers: Command | Shift},
		{menu: "Edit", item: "Cut", action: "cut:", key: "x", modifiers: Command},
		{menu: "Edit", item: "Copy", action: "copy:", key: "c", modifiers: Command},
		{menu: "Edit", item: "Paste", action: "paste:", key: "v", modifiers: Command},
		{menu: "Edit", item: "Select All", action: "selectAll:", key: "a", modifiers: Command},
		{menu: "View", item: "Enter Full Screen", action: "toggleFullScreen:", key: "f",
			modifiers: Command | Control},
		{menu: "Window", item: "Minimize", action: "performMiniaturize:", key: "m", modifiers: Command},
		{menu: "Window", item: "Close Window", action: "performClose:", key: "w", modifiers: Command},
	}
	menus := Standard("HomeShell")
	for _, tt := range tests {
		t.Run(tt.menu+"/"+tt.item, func(t *testing.T) {
			item, ok := findItem(menus, tt.menu, tt.item)
			if !ok {
				t.Fatalf("no %q item in the %q menu", tt.item, tt.menu)
			}
			if item.Action != tt.action || item.Key != tt.key || item.Modifiers != tt.modifiers {
				t.Errorf("%q = {%q %q %#x}, want {%q %q %#x}", tt.item,
					item.Action, item.Key, item.Modifiers, tt.action, tt.key, tt.modifiers)
			}
		})
	}
}

func TestStandardNamesTheAppMenuAfterTheApp(t *testing.T) {
	menus := Standard("Example")
	if len(menus) == 0 || menus[0].Title != "Example" {
		t.Fatalf("first menu = %+v, want the app menu titled Example", menus)
	}
	if _, ok := findItem(menus, "Example", "Quit Example"); !ok {
		t.Error("app menu has no Quit Example item")
	}
}

// Two items answering the same shortcut would leave AppKit to pick one of them.
func TestStandardHasNoDuplicateShortcuts(t *testing.T) {
	type shortcut struct {
		key       string
		modifiers Modifier
	}
	seen := map[shortcut]string{}
	for _, menu := range Standard("HomeShell") {
		for _, item := range menu.Items {
			if item.Key == "" {
				continue
			}
			binding := shortcut{key: item.Key, modifiers: item.Modifiers}
			if previous, ok := seen[binding]; ok {
				t.Errorf("%q and %q share the same shortcut", previous, item.Title)
			}
			seen[binding] = item.Title
		}
	}
}

func TestStandardSeparatorsCarryNothing(t *testing.T) {
	for _, menu := range Standard("HomeShell") {
		for _, item := range menu.Items {
			if item.Separator && (item.Title != "" || item.Action != "" || item.Key != "") {
				t.Errorf("%s menu has a separator with content: %+v", menu.Title, item)
			}
			if !item.Separator && (item.Title == "" || item.Action == "") {
				t.Errorf("%s menu has an item without a title or action: %+v", menu.Title, item)
			}
		}
	}
}

func TestStandardHasOneWindowsMenu(t *testing.T) {
	var windowsMenus []string
	for _, menu := range Standard("HomeShell") {
		if menu.Windows {
			windowsMenus = append(windowsMenus, menu.Title)
		}
	}
	if len(windowsMenus) != 1 || windowsMenus[0] != "Window" {
		t.Errorf("windows menus = %v, want [Window]", windowsMenus)
	}
}
