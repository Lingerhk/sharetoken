package main

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
int stTrayIconShown(void);
int stWindowShown(void *window);
void stCloseShortcut(void);
*/
import "C"

import "github.com/wailsapp/wails/v3/pkg/application"

// windowShown: the window is open, not minimised, and the app is not hidden.
// Wails' IsVisible asks whether any of the window can be seen, which is false
// while other apps' windows cover it.
func windowShown(w *application.WebviewWindow) bool { return C.stWindowShown(w.NativeWindow()) != 0 }

func nativeTrayIconShown() bool { return C.stTrayIconShown() != 0 }

func nativeCloseShortcut() { C.stCloseShortcut() }
