// The menu-bar item (NSStatusItem) of the macOS app; see tray_darwin.go.
#import <Cocoa/Cocoa.h>
#include <stdlib.h>
#include <string.h>
#include "_cgo_export.h"

@interface STTrayTarget : NSObject
- (void)open:(id)sender;
- (void)quit:(id)sender;
@end

@implementation STTrayTarget
- (void)open:(id)sender { stTrayClickedOpen(); }
- (void)quit:(id)sender { stTrayClickedQuit(); }
@end

static NSStatusItem *statusItem;
static NSMenuItem *statusLine, *updateItem, *openItem, *quitItem;
// The lines under the status line; as many as trayInfoLines in tray.go.
#define ST_INFO_LINES 3
static NSMenuItem *infoLines[ST_INFO_LINES];
static STTrayTarget *target;

static void onMain(dispatch_block_t block) {
    if ([NSThread isMainThread]) {
        block();
    } else {
        dispatch_async(dispatch_get_main_queue(), block);
    }
}

void stTrayStart(const void *png, int length, const char *tooltip) {
    NSData *data = [NSData dataWithBytes:png length:length];
    NSString *tip = [NSString stringWithUTF8String:tooltip];
    onMain(^{
        target = [STTrayTarget new];
        NSImage *image = [[NSImage alloc] initWithData:data];
        image.size = NSMakeSize(18, 18);
        image.template = YES; // the system colors it for a light or dark menu bar

        NSMenu *menu = [NSMenu new];
        menu.autoenablesItems = NO;
        statusLine = [menu addItemWithTitle:tip action:nil keyEquivalent:@""];
        statusLine.enabled = NO;
        for (int i = 0; i < ST_INFO_LINES; i++) {
            infoLines[i] = [menu addItemWithTitle:@"" action:nil keyEquivalent:@""];
            infoLines[i].enabled = NO;
            infoLines[i].hidden = YES;
        }
        updateItem = [menu addItemWithTitle:@"" action:@selector(open:) keyEquivalent:@""];
        updateItem.target = target;
        updateItem.hidden = YES;
        [menu addItem:[NSMenuItem separatorItem]];
        openItem = [menu addItemWithTitle:tip action:@selector(open:) keyEquivalent:@""];
        openItem.target = target;
        [menu addItem:[NSMenuItem separatorItem]];
        quitItem = [menu addItemWithTitle:tip action:@selector(quit:) keyEquivalent:@""];
        quitItem.target = target;

        statusItem = [[NSStatusBar systemStatusBar] statusItemWithLength:NSSquareStatusItemLength];
        statusItem.button.image = image;
        statusItem.button.toolTip = tip;
        statusItem.menu = menu;
    });
}

void stTraySet(const char *status, const char *info, const char *update, const char *open, const char *quit, const char *tooltip) {
    NSString *s = [NSString stringWithUTF8String:status];
    NSString *lines = [NSString stringWithUTF8String:info];
    NSArray<NSString *> *is = lines.length == 0 ? @[] : [lines componentsSeparatedByString:@"\n"];
    NSString *tip = [NSString stringWithUTF8String:tooltip];
    NSString *u = [NSString stringWithUTF8String:update];
    NSString *o = [NSString stringWithUTF8String:open];
    NSString *q = [NSString stringWithUTF8String:quit];
    onMain(^{
        statusLine.title = s;
        for (NSUInteger i = 0; i < ST_INFO_LINES; i++) {
            infoLines[i].hidden = i >= is.count;
            if (i < is.count) {
                infoLines[i].title = is[i];
            }
        }
        if (tip.length > 0) {
            statusItem.button.toolTip = tip;
        }
        updateItem.title = u;
        updateItem.hidden = u.length == 0;
        openItem.title = o;
        quitItem.title = q;
    });
}

void stTrayStop(void) {
    onMain(^{
        if (statusItem != nil) {
            [[NSStatusBar systemStatusBar] removeStatusItem:statusItem];
            statusItem = nil;
        }
    });
}

void stTrayRaise(void) {
    onMain(^{
        [NSApp unhide:nil];
        for (NSWindow *w in NSApp.windows) {
            if (w.miniaturized) {
                [w deminiaturize:nil];
            }
        }
    });
}

// The rest is for the tray self-test (tray_selftest.go).

typedef struct { int iconShown, windowVisible; char *status, *open, *quit; } stTrayState;

static NSWindow *appWindow(void) {
    for (NSWindow *w in NSApp.windows) {
        if ([w isKindOfClass:NSClassFromString(@"WailsWindow")]) {
            return w;
        }
    }
    return nil;
}

// drawnPixels draws the menu-bar button off screen and counts what shows.
static int drawnPixels(NSStatusBarButton *button) {
    NSBitmapImageRep *rep = [button bitmapImageRepForCachingDisplayInRect:button.bounds];
    if (rep == nil) {
        return 0;
    }
    [button cacheDisplayInRect:button.bounds toBitmapImageRep:rep];
    int n = 0;
    for (NSInteger y = 0; y < rep.pixelsHigh; y++) {
        for (NSInteger x = 0; x < rep.pixelsWide; x++) {
            if ([rep colorAtX:x y:y].alphaComponent > 0.5) {
                n++;
            }
        }
    }
    return n;
}

stTrayState stTrayProbe(void) {
    __block stTrayState s = {0};
    dispatch_sync(dispatch_get_main_queue(), ^{
        NSStatusBarButton *button = statusItem.button;
        s.iconShown = button != nil && button.window != nil && statusItem.isVisible && drawnPixels(button) > 20;
        NSWindow *w = appWindow();
        s.windowVisible = w != nil && w.visible && !w.miniaturized && !NSApp.hidden;
        s.status = strdup(statusLine.title.UTF8String ?: "");
        s.open = strdup(openItem.title.UTF8String ?: "");
        s.quit = strdup(quitItem.title.UTF8String ?: "");
    });
    return s;
}

void stTrayClick(int open) {
    dispatch_async(dispatch_get_main_queue(), ^{
        NSMenu *menu = statusItem.menu;
        [menu performActionForItemAtIndex:[menu indexOfItem:open ? openItem : quitItem]];
    });
}

void stWindowClose(void) {
    dispatch_async(dispatch_get_main_queue(), ^{ [appWindow() performClose:nil]; });
}

// stCloseShortcut gives the app's menu the key event for Command-W.
void stCloseShortcut(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        NSEvent *e = [NSEvent keyEventWithType:NSEventTypeKeyDown location:NSZeroPoint modifierFlags:NSEventModifierFlagCommand
                                     timestamp:0 windowNumber:appWindow().windowNumber context:nil
                                    characters:@"w" charactersIgnoringModifiers:@"w" isARepeat:NO keyCode:13];
        [NSApp.mainMenu performKeyEquivalent:e];
    });
}
