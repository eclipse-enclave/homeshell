// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build darwin

package appmenu

import (
	"os"
	"reflect"
	"runtime"
	"testing"
)

// AppKit objects belong to the main thread, but tests run on other goroutines,
// so TestMain keeps the main thread and runs each AppKit call there.
var onMainThread = make(chan func())

func init() {
	runtime.LockOSThread()
}

func TestMain(m *testing.M) {
	exitCode := make(chan int)
	go func() {
		exitCode <- m.Run()
	}()
	for {
		select {
		case work := <-onMainThread:
			work()
		case code := <-exitCode:
			os.Exit(code)
		}
	}
}

func onMain(work func()) {
	done := make(chan struct{})
	onMainThread <- func() {
		defer close(done)
		work()
	}
	<-done
}

func TestInstallPutsTheStandardMenusInTheMenuBar(t *testing.T) {
	var installed []Menu
	onMain(func() {
		Install("HomeShell", func() {})
		installed = installedMenus()
	})
	want := Standard("HomeShell")
	if !reflect.DeepEqual(installed, want) {
		t.Errorf("installed menus =\n%+v\nwant\n%+v", installed, want)
	}
}

// The reported bug: Cmd+Q did nothing because no menu item answered it.
func TestCommandQCallsTheQuitFunction(t *testing.T) {
	quits := 0
	var handled bool
	onMain(func() {
		Install("HomeShell", func() { quits++ })
		handled = performKeyEquivalent("q", Command)
	})
	if !handled {
		t.Error("the main menu did not handle Cmd+Q")
	}
	if quits != 1 {
		t.Errorf("quit function called %d times, want 1", quits)
	}
}

func TestInstallingAgainReplacesTheQuitFunction(t *testing.T) {
	first, second := 0, 0
	onMain(func() {
		Install("HomeShell", func() { first++ })
		Install("HomeShell", func() { second++ })
		performKeyEquivalent("q", Command)
	})
	if first != 0 || second != 1 {
		t.Errorf("quit calls = (first %d, second %d), want (0, 1)", first, second)
	}
}

// Quit from the Dock menu, logout, and AppleScript arrive as a quit Apple
// event, not through the menu bar.
func TestQuitAppleEventCallsTheQuitFunction(t *testing.T) {
	quits := 0
	var handled bool
	onMain(func() {
		Install("HomeShell", func() { quits++ })
		handled = dispatchQuitAppleEvent()
	})
	if !handled {
		t.Error("no handler accepted the quit Apple event")
	}
	if quits != 1 {
		t.Errorf("quit function called %d times, want 1", quits)
	}
}
