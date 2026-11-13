package main

import (
	"fmt"
	"unsafe"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
)

var (
	shell32         = windows.NewLazySystemDLL("shell32.dll")
	procPostMessage = user32.NewProc("PostMessageW")
	procNotifyIcon  = shell32.NewProc("Shell_NotifyIconW")
)

const (
	wmClose, wmCommand, wmLButtonUp = 0x0010, 0x0111, 0x0202
	// The tray library's icon id and callback message (systray_windows.go).
	trayIconID, trayCallback = 100, 0x0400 + 1
)

func trayWindow() windows.HWND { return ownWindow("SystrayClass") }

// iconAdded asks the shell to change nothing about the icon, which it refuses if
// it does not have one. Where the icon is cannot be asked: Windows 11 keeps a new
// icon in the overflow flyout, and has no position for it while that is closed.
func iconAdded(tray windows.HWND) bool {
	// NOTIFYICONDATAW: cbSize, then hWnd and uID at their 64-bit offsets; no flags.
	var data [976]byte
	*(*uint32)(unsafe.Pointer(&data[0])) = uint32(len(data))
	*(*windows.HWND)(unsafe.Pointer(&data[8])) = tray
	*(*uint32)(unsafe.Pointer(&data[16])) = trayIconID
	const nimModify = 1
	r, _, _ := procNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&data[0])))
	return r != 0
}

func nativeTrayProbe() trayProbe {
	winTray.mu.Lock()
	m := winTray.shown
	winTray.mu.Unlock()
	p := trayProbe{Status: m.Status, Open: m.Open, Quit: m.Quit}
	if t := trayWindow(); t != 0 {
		p.IconShown = iconAdded(t)
	}
	if w := appWindow(); w != 0 {
		iconic, _, _ := procIsIconic.Call(uintptr(w))
		p.WindowVisible = windows.IsWindowVisible(w) && iconic == 0
	}
	return p
}

func menuItemID(item *systray.MenuItem) uintptr {
	var id uint32
	var title string
	fmt.Sscanf(item.String(), "MenuItem[%d, %q]", &id, &title)
	return uintptr(id)
}

// nativeTrayClick posts the messages the shell and the menu send for a click.
func nativeTrayClick(what string) {
	t := trayWindow()
	switch what {
	case "icon":
		procPostMessage.Call(uintptr(t), trayCallback, trayIconID, wmLButtonUp)
	case "open":
		procPostMessage.Call(uintptr(t), wmCommand, menuItemID(winTray.open), 0)
	case "quit":
		procPostMessage.Call(uintptr(t), wmCommand, menuItemID(winTray.quit), 0)
	}
}

func nativeWindowClose() { procPostMessage.Call(uintptr(appWindow()), wmClose, 0, 0) }

func nativeCloseShortcut() {}
