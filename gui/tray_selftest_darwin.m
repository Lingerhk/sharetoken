// The tray self-test's view of the menu-bar item (tray_selftest.go).
#import <Cocoa/Cocoa.h>

// drawnPixels draws a view off screen and counts what shows.
static int drawnPixels(NSView *view) {
    NSBitmapImageRep *rep = [view bitmapImageRepForCachingDisplayInRect:view.bounds];
    if (rep == nil) {
        return 0;
    }
    [view cacheDisplayInRect:view.bounds toBitmapImageRep:rep];
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

// stTrayIconShown: the app's menu-bar item is on screen with its icon drawn.
// The item lives in a window of its own, of the class NSStatusBarWindow.
int stTrayIconShown(void) {
    __block int shown = 0;
    dispatch_sync(dispatch_get_main_queue(), ^{
        for (NSWindow *w in NSApp.windows) {
            if ([NSStringFromClass([w class]) isEqualToString:@"NSStatusBarWindow"] && w.visible && drawnPixels(w.contentView) > 20) {
                shown = 1;
            }
        }
    });
    return shown;
}

// stCloseShortcut gives the app's menu the key event for Command-W.
void stCloseShortcut(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        NSWindow *key = NSApp.keyWindow ?: NSApp.mainWindow;
        NSEvent *e = [NSEvent keyEventWithType:NSEventTypeKeyDown location:NSZeroPoint modifierFlags:NSEventModifierFlagCommand
                                     timestamp:0 windowNumber:key.windowNumber context:nil
                                    characters:@"w" charactersIgnoringModifiers:@"w" isARepeat:NO keyCode:13];
        [NSApp.mainMenu performKeyEquivalent:e];
    });
}
