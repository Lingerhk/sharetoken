package main

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	shell32          = windows.NewLazySystemDLL("shell32.dll")
	procFindWindowEx = user32.NewProc("FindWindowExW")
	procPostMessage  = user32.NewProc("PostMessageW")
	procNotifyIcon   = shell32.NewProc("Shell_NotifyIconW")
)

const (
	wmLButtonUp = 0x0202
	// Wails' tray: its window is a message-only one of the app's window class,
	// the first tray's icon id is 1, and the shell calls it back with WM_USER+1
	// (systemtray_windows.go).
	trayWindowClass          = "WailsWebviewWindow"
	trayIconID, trayCallback = 1, 0x0400 + 1
)

// trayWindow finds this process's message-only window of Wails' class.
func trayWindow() windows.HWND {
	const hwndMessage = ^uintptr(2) // -3
	class, _ := windows.UTF16PtrFromString(trayWindowClass)
	var after uintptr
	for {
		h, _, _ := procFindWindowEx.Call(hwndMessage, after, uintptr(unsafe.Pointer(class)), 0)
		if h == 0 {
			return 0
		}
		var pid uint32
		if _, err := windows.GetWindowThreadProcessId(windows.HWND(h), &pid); err == nil && pid == uint32(os.Getpid()) {
			return windows.HWND(h)
		}
		after = h
	}
}

// nativeTrayIconShown asks the shell to change nothing about the icon, which it
// refuses if it does not have one. Where the icon is cannot be asked: Windows 11
// keeps a new icon in the overflow flyout, and has no position for it while that
// is closed.
func nativeTrayIconShown() bool {
	tray := trayWindow()
	if tray == 0 {
		return false
	}
	// NOTIFYICONDATAW: cbSize, then hWnd and uID at their 64-bit offsets; no flags.
	var data [976]byte
	*(*uint32)(unsafe.Pointer(&data[0])) = uint32(len(data))
	*(*windows.HWND)(unsafe.Pointer(&data[8])) = tray
	*(*uint32)(unsafe.Pointer(&data[16])) = trayIconID
	const nimModify = 1
	r, _, _ := procNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&data[0])))
	return r != 0
}

// nativeTrayClickIcon posts what the shell sends for a click on the icon
// (NOTIFYICON_VERSION_4: the event in the low word, the icon id in the high one).
func nativeTrayClickIcon() {
	procPostMessage.Call(uintptr(trayWindow()), trayCallback, 0, wmLButtonUp|trayIconID<<16)
}

func nativeCloseShortcut() {}
