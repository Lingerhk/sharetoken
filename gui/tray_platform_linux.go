package main

import (
	_ "embed"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed packaging/tray.png
var trayIcon []byte

// Desktops show a status notifier item's menu on a click.
const trayClickOpens = false

// trayAvailable: the desktop shows status notifier items (KDE, Ubuntu's GNOME
// with its AppIndicator extension, and others). Where it does not, as on a plain
// GNOME, a closed window could not be opened again, so closing quits.
func trayAvailable() bool {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false
	}
	defer conn.Close()
	var owned bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&owned)
	return err == nil && owned
}

func setTrayIcon(t *application.SystemTray) { t.SetIcon(trayIcon) }

// menuText escapes _ , which a dbusmenu label reads as a mnemonic mark.
func menuText(s string) string { return strings.ReplaceAll(s, "_", "__") }

func nativeTrayClickIcon() {}
