package main

import (
	_ "embed"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed packaging/tray_template.png
var trayIcon []byte

// A click on a menu-bar item opens its menu.
const trayClickOpens = false

func trayAvailable() bool { return true }

// The template image is black with alpha; the system colors it for a light or
// dark menu bar.
func setTrayIcon(t *application.SystemTray) { t.SetTemplateIcon(trayIcon) }

func menuText(s string) string { return s }

func nativeTrayClickIcon() {}
