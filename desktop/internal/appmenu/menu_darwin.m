// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

#import <Cocoa/Cocoa.h>

#include "menu_darwin.h"
#include "_cgo_export.h"

// Marks the items this package adds, so appmenuDescribe skips the ones AppKit
// adds on its own (such as Emoji & Symbols in the Edit menu).
static const NSInteger appmenuTag = 0x484f4d45;

// Quits for the Quit menu item and for the quit Apple event, which the Dock
// menu, logout and AppleScript send.
@interface HomeShellQuitTarget : NSObject
- (void)quit:(id)sender;
- (void)handleQuitEvent:(NSAppleEventDescriptor *)event withReplyEvent:(NSAppleEventDescriptor *)reply;
@end

@implementation HomeShellQuitTarget
- (void)quit:(id)sender {
  appmenuQuit();
}

- (void)handleQuitEvent:(NSAppleEventDescriptor *)event withReplyEvent:(NSAppleEventDescriptor *)reply {
  appmenuQuit();
}
@end

// Menu items and the Apple event manager hold their target weakly, so the quit
// target lives for the process.
static HomeShellQuitTarget *quitTarget;
static NSMenu *menuBar;
static NSMenu *currentMenu;
static NSMenu *windowsMenu;

static NSString *toNSString(const char *utf8) {
  return [NSString stringWithUTF8String:utf8];
}

void appmenuBegin(void) {
  @autoreleasepool {
    [NSApplication sharedApplication];
    if (quitTarget == nil) {
      quitTarget = [[HomeShellQuitTarget alloc] init];
    }
    [menuBar release];
    menuBar = [[NSMenu alloc] initWithTitle:@""];
    currentMenu = nil;
    windowsMenu = nil;
  }
}

void appmenuAddMenu(const char *title, bool windows) {
  @autoreleasepool {
    NSString *name = toNSString(title);
    NSMenu *menu = [[[NSMenu alloc] initWithTitle:name] autorelease];
    NSMenuItem *holder = [[[NSMenuItem alloc] initWithTitle:name action:nil keyEquivalent:@""] autorelease];
    holder.submenu = menu;
    [menuBar addItem:holder];
    currentMenu = menu;
    if (windows) {
      windowsMenu = menu;
    }
  }
}

void appmenuAddItem(const char *title, const char *action, const char *key, unsigned long modifiers) {
  @autoreleasepool {
    SEL selector = NSSelectorFromString(toNSString(action));
    NSMenuItem *item = [[[NSMenuItem alloc] initWithTitle:toNSString(title)
                                                   action:selector
                                            keyEquivalent:toNSString(key)] autorelease];
    item.keyEquivalentModifierMask = modifiers;
    item.tag = appmenuTag;
    if (selector == @selector(quit:)) {
      item.target = quitTarget;
    }
    [currentMenu addItem:item];
  }
}

void appmenuAddSeparator(void) {
  @autoreleasepool {
    NSMenuItem *item = [NSMenuItem separatorItem];
    item.tag = appmenuTag;
    [currentMenu addItem:item];
  }
}

void appmenuInstall(void) {
  @autoreleasepool {
    [NSApp setMainMenu:menuBar];
    if (windowsMenu != nil) {
      [NSApp setWindowsMenu:windowsMenu];
    }
    // Replaces AppKit's own handler, which calls terminate: and so exit().
    [[NSAppleEventManager sharedAppleEventManager] setEventHandler:quitTarget
                                                       andSelector:@selector(handleQuitEvent:withReplyEvent:)
                                                     forEventClass:kCoreEventClass
                                                        andEventID:kAEQuitApplication];
  }
}

char *appmenuDescribe(void) {
  @autoreleasepool {
    NSMutableString *description = [NSMutableString string];
    for (NSMenuItem *holder in [NSApp mainMenu].itemArray) {
      NSMenu *menu = holder.submenu;
      [description appendFormat:@"menu\t%@\t%d\n", menu.title, menu == [NSApp windowsMenu]];
      for (NSMenuItem *item in menu.itemArray) {
        if (item.tag != appmenuTag) {
          continue;
        }
        if (item.isSeparatorItem) {
          [description appendString:@"separator\n"];
          continue;
        }
        [description appendFormat:@"item\t%@\t%@\t%@\t%lu\n", item.title, NSStringFromSelector(item.action),
                                  item.keyEquivalent, (unsigned long)item.keyEquivalentModifierMask];
      }
    }
    return strdup(description.UTF8String);
  }
}

bool appmenuPerformKeyEquivalent(const char *key, unsigned long modifiers) {
  @autoreleasepool {
    NSString *characters = toNSString(key);
    NSEvent *event = [NSEvent keyEventWithType:NSEventTypeKeyDown
                                      location:NSZeroPoint
                                 modifierFlags:modifiers
                                     timestamp:0
                                  windowNumber:0
                                       context:nil
                                    characters:characters
                   charactersIgnoringModifiers:characters
                                     isARepeat:NO
                                       keyCode:0];
    return [[NSApp mainMenu] performKeyEquivalent:event];
  }
}

bool appmenuDispatchQuitAppleEvent(void) {
  @autoreleasepool {
    NSAppleEventDescriptor *event =
        [NSAppleEventDescriptor appleEventWithEventClass:kCoreEventClass
                                                 eventID:kAEQuitApplication
                                        targetDescriptor:[NSAppleEventDescriptor currentProcessDescriptor]
                                                returnID:kAutoGenerateReturnID
                                           transactionID:kAnyTransactionID];
    AppleEvent reply = {typeNull, NULL};
    // Cocoa handlers ignore the refcon, which must not be null.
    OSErr err = [[NSAppleEventManager sharedAppleEventManager] dispatchRawAppleEvent:event.aeDesc
                                                                         withRawReply:&reply
                                                                        handlerRefCon:&reply];
    AEDisposeDesc(&reply);
    return err == noErr;
  }
}
