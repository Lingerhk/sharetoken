package main

import (
	"os"
	"strings"

	"github.com/godbus/dbus/v5"
)

// nativeTrayIconShown: the desktop's status notifier watcher lists an item of
// this process. Whether the panel draws it is up to the desktop.
func nativeTrayIconShown() bool {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false
	}
	defer conn.Close()
	v, err := conn.Object("org.kde.StatusNotifierWatcher", "/StatusNotifierWatcher").GetProperty("org.kde.StatusNotifierWatcher.RegisteredStatusNotifierItems")
	if err != nil {
		return false
	}
	items, _ := v.Value().([]string)
	for _, it := range items {
		// ":1.42/StatusNotifierItem", ":1.42@/StatusNotifierItem" or a well-known name
		name, _, _ := strings.Cut(strings.SplitN(it, "/", 2)[0], "@")
		var pid uint32
		if conn.BusObject().Call("org.freedesktop.DBus.GetConnectionUnixProcessID", 0, name).Store(&pid) == nil && int(pid) == os.Getpid() {
			return true
		}
	}
	return false
}

func nativeCloseShortcut() {}
