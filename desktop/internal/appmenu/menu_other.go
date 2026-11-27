// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

//go:build !darwin

package appmenu

// Install does nothing outside macOS, where the window manager handles quitting
// and the webview handles editing shortcuts itself.
func Install(appName string, quit func()) {}
