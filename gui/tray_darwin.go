package main

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
void stTrayStart(const void *png, int length, const char *tooltip);
void stTraySet(const char *status, const char *update, const char *open, const char *quit);
void stTrayStop(void);
void stTrayRaise(void);
*/
import "C"

import (
	"context"
	_ "embed"
	"unsafe"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const trayAvailable = true

//go:embed packaging/tray_template.png
var trayIcon []byte

var trayOnOpen, trayOnQuit func()

// nativeTrayStart adds the menu-bar item with AppKit directly: the common Go tray
// libraries define their own AppDelegate class, which clashes with Wails' at link
// time. The menu opens on a click, as it does for other menu-bar items.
func nativeTrayStart(tooltip string, onOpen, onQuit func()) {
	trayOnOpen, trayOnQuit = onOpen, onQuit
	tip := C.CString(tooltip)
	defer C.free(unsafe.Pointer(tip))
	C.stTrayStart(unsafe.Pointer(&trayIcon[0]), C.int(len(trayIcon)), tip)
}

func nativeTraySet(m trayMenu) {
	status, update, open, quit := C.CString(m.Status), C.CString(m.Update), C.CString(m.Open), C.CString(m.Quit)
	defer C.free(unsafe.Pointer(status))
	defer C.free(unsafe.Pointer(update))
	defer C.free(unsafe.Pointer(open))
	defer C.free(unsafe.Pointer(quit))
	C.stTraySet(status, update, open, quit)
}

func nativeTrayStop() { C.stTrayStop() }

// nativeShowWindow undoes what closing did: Wails hides the whole app on close,
// and showing the window alone does not always bring a hidden app back.
func nativeShowWindow(ctx context.Context) {
	C.stTrayRaise()
	runtime.WindowShow(ctx)
}

// These run on the main thread, which Wails needs back to show the window.
//
//export stTrayClickedOpen
func stTrayClickedOpen() { go trayOnOpen() }

//export stTrayClickedQuit
func stTrayClickedQuit() { go trayOnQuit() }
