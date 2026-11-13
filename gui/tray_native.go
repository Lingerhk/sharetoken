package main

import (
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// nativeTray is the tray icon (Windows), menu-bar item (macOS) or status
// notifier item (Linux) and its menu, through Wails. The menu: the status line
// and up to trayInfoLines lines under it (not clickable), the update item when
// there is one, Open, Quit.
type nativeTray struct {
	tray                       *application.SystemTray
	menu                       *application.Menu
	status, update, open, quit *application.MenuItem
	info                       [trayInfoLines]*application.MenuItem
	onOpen, onQuit             func()

	mu    sync.Mutex
	shown trayMenu // for the self-test
}

func startTray(app *application.App, tooltip string, onOpen, onQuit func()) *nativeTray {
	t := &nativeTray{tray: app.SystemTray.New(), menu: app.NewMenu(), onOpen: onOpen, onQuit: onQuit}
	setTrayIcon(t.tray)
	t.tray.SetTooltip(tooltip)
	t.status = t.menu.Add(tooltip).SetEnabled(false)
	for i := range t.info {
		t.info[i] = t.menu.Add("").SetEnabled(false).SetHidden(true)
	}
	t.update = t.menu.Add("").SetHidden(true).OnClick(func(*application.Context) { go onOpen() })
	t.menu.AddSeparator()
	t.open = t.menu.Add(tooltip).OnClick(func(*application.Context) { go onOpen() })
	t.menu.AddSeparator()
	t.quit = t.menu.Add(tooltip).OnClick(func(*application.Context) { go onQuit() })
	t.tray.SetMenu(t.menu)
	if trayClickOpens { // elsewhere a click opens the menu
		t.tray.OnClick(func() { go onOpen() })
	}
	return t
}

func (t *nativeTray) set(m trayMenu) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.shown = m
	t.status.SetLabel(menuText(m.Status))
	for i, item := range t.info {
		if i < len(m.Info) {
			item.SetLabel(menuText(m.Info[i])).SetHidden(false)
		} else {
			item.SetHidden(true)
		}
	}
	if m.Update == "" {
		t.update.SetHidden(true)
	} else {
		t.update.SetLabel(menuText(m.Update)).SetHidden(false)
	}
	t.open.SetLabel(menuText(m.Open))
	t.quit.SetLabel(menuText(m.Quit))
	t.menu.Update()
	if m.Tooltip != "" {
		t.tray.SetTooltip(m.Tooltip)
	}
}

func (t *nativeTray) shownMenu() trayMenu {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.shown
}

// click does what the menu's items do, for the self-test. Wails has no way to
// press a menu item from outside; the icon click on Windows goes through the
// real message (nativeTrayClickIcon).
func (t *nativeTray) click(what string) {
	switch what {
	case "open":
		go t.onOpen()
	case "quit":
		go t.onQuit()
	case "icon":
		nativeTrayClickIcon()
	}
}
