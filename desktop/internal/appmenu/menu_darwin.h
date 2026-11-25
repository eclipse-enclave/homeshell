// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

#ifndef HOMESHELL_APPMENU_DARWIN_H
#define HOMESHELL_APPMENU_DARWIN_H

#include <stdbool.h>

// Building a menu bar: begin, then each menu followed by its items, then
// install. Every call must be on the main thread.
void appmenuBegin(void);
void appmenuAddMenu(const char *title, bool windows);
void appmenuAddItem(const char *title, const char *action, const char *key, unsigned long modifiers);
void appmenuAddSeparator(void);
void appmenuInstall(void);

// For tests: the installed menu bar as tab-separated lines (caller frees), a
// synthesized key-down event sent through it, and a quit Apple event sent
// through the Apple event manager.
char *appmenuDescribe(void);
bool appmenuPerformKeyEquivalent(const char *key, unsigned long modifiers);
bool appmenuDispatchQuitAppleEvent(void);

#endif
