package main

import (
	"context"
	_ "embed"
	"os"
	"runtime"
	"strings"
	"sync"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
)

const trayAvailable = true

//go:embed packaging/tray.ico
var trayIcon []byte

var winTray struct {
	mu             sync.Mutex
	status, update *systray.MenuItem
	open, quit     *systray.MenuItem
	info           [trayInfoLines]*systray.MenuItem
	pending        *trayMenu
	shown          trayMenu // for the self-test
}

// menuText escapes & , which a Windows menu reads as an accelerator mark.
func menuText(s string) string { return strings.ReplaceAll(s, "&", "&&") }

// nativeTrayStart runs the tray on a thread of its own: the icon's hidden window
// belongs to the thread that creates it, and its message loop must not be the
// window's. A click on the icon opens the window; the right button opens the menu.
func nativeTrayStart(tooltip string, onOpen, onQuit func()) {
	guardWindow()
	systray.SetOnTapped(func() { go onOpen() })
	go func() {
		runtime.LockOSThread()
		systray.Run(func() {
			systray.SetIcon(trayIcon)
			systray.SetTooltip(tooltip)
			status := systray.AddMenuItem("sharetoken", "")
			status.Disable()
			var info [trayInfoLines]*systray.MenuItem
			for i := range info {
				info[i] = systray.AddMenuItem("info", "")
				info[i].Disable()
				info[i].Hide()
			}
			update := systray.AddMenuItem("update", "")
			update.Hide()
			systray.AddSeparator()
			open := systray.AddMenuItem(tooltip, "")
			systray.AddSeparator()
			quit := systray.AddMenuItem(tooltip, "")
			winTray.mu.Lock()
			winTray.status, winTray.update, winTray.open, winTray.quit, winTray.info = status, update, open, quit, info
			pending := winTray.pending
			winTray.mu.Unlock()
			if pending != nil {
				nativeTraySet(*pending)
			}
			go func() {
				for {
					select {
					case <-open.ClickedCh:
						onOpen()
					case <-update.ClickedCh:
						onOpen()
					case <-quit.ClickedCh:
						onQuit()
					}
				}
			}()
		}, nil)
	}()
}

func nativeTraySet(m trayMenu) {
	winTray.mu.Lock()
	defer winTray.mu.Unlock()
	if winTray.status == nil {
		winTray.pending = &m
		return
	}
	winTray.shown = m
	winTray.status.SetTitle(menuText(m.Status))
	for i, item := range winTray.info {
		if i < len(m.Info) {
			item.SetTitle(menuText(m.Info[i]))
			item.Show()
		} else {
			item.Hide()
		}
	}
	if m.Tooltip != "" {
		systray.SetTooltip(m.Tooltip)
	}
	if m.Update == "" {
		winTray.update.Hide()
	} else {
		winTray.update.SetTitle(menuText(m.Update))
		winTray.update.Show()
	}
	winTray.open.SetTitle(menuText(m.Open))
	winTray.quit.SetTitle(menuText(m.Quit))
}

// nativeTrayStop removes the icon, which Windows would otherwise leave behind
// until the mouse passes over it.
func nativeTrayStop() { systray.Quit() }

// enumOwn is one callback for every search: Windows never frees the slot of one.
var (
	enumMu    sync.Mutex
	enumClass string
	enumFound windows.HWND
	enumOwn   = windows.NewCallback(func(h windows.HWND, _ uintptr) uintptr {
		var pid uint32
		if _, err := windows.GetWindowThreadProcessId(h, &pid); err != nil || pid != uint32(os.Getpid()) {
			return 1
		}
		buf := make([]uint16, 64)
		if n, err := windows.GetClassName(h, &buf[0], int32(len(buf))); err == nil && windows.UTF16ToString(buf[:n]) == enumClass {
			enumFound = h
			return 0
		}
		return 1
	})
)

// ownWindow finds this process's top-level window of a class.
func ownWindow(class string) windows.HWND {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumClass, enumFound = class, 0
	_ = windows.EnumWindows(enumOwn, nil)
	return enumFound
}

func appWindow() windows.HWND { return ownWindow("wailsWindow") }

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procIsIconic            = user32.NewProc("IsIconic")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

var (
	procIsWindowVisible   = user32.NewProc("IsWindowVisible")
	procCallWindowProc    = user32.NewProc("CallWindowProcW")
	procSetWindowLongPtr  = user32.NewProc("SetWindowLongPtrW")
	originalWindowProc    uintptr
	guardedWindowProcSlot = windows.NewCallback(guardedWindowProc)
)

// guardedWindowProc comes before Wails' window procedure. On every WM_SETFOCUS
// Wails moves the focus into WebView2, and if WebView2 refuses, as it does for a
// window that is minimised or hidden, Wails' error handler ends the program.
// Such a window has nothing to focus, so the message stops here.
func guardedWindowProc(h windows.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	const wmSetFocus = 0x0007
	if msg == wmSetFocus {
		iconic, _, _ := procIsIconic.Call(uintptr(h))
		visible, _, _ := procIsWindowVisible.Call(uintptr(h))
		if iconic != 0 || visible == 0 {
			return 0
		}
	}
	r, _, _ := procCallWindowProc.Call(originalWindowProc, uintptr(h), uintptr(msg), wParam, lParam)
	return r
}

// guardWindow installs guardedWindowProc on the app's window.
func guardWindow() {
	const gwlpWndProc = ^uintptr(3) // -4
	w := appWindow()
	if w == 0 || originalWindowProc != 0 {
		return
	}
	originalWindowProc, _, _ = procSetWindowLongPtr.Call(uintptr(w), gwlpWndProc, guardedWindowProcSlot)
}

// nativeShowWindow does not use Wails' WindowShow, which calls SetFocus on the
// window itself. Activating the window gives it the focus the way the taskbar does.
func nativeShowWindow(context.Context) {
	const swShow, swRestore = 5, 9
	w := appWindow()
	if w == 0 {
		return
	}
	cmd := uintptr(swShow)
	if iconic, _, _ := procIsIconic.Call(uintptr(w)); iconic != 0 {
		cmd = swRestore
	}
	procShowWindow.Call(uintptr(w), cmd)
	procSetForegroundWindow.Call(uintptr(w))
}
