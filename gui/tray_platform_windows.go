package main

import (
	_ "embed"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed packaging/tray.ico
var trayIcon []byte

// A click on the icon opens the window; the right button opens the menu.
const trayClickOpens = true

func trayAvailable() bool { return true }

// The .ico holds each size the shell may ask for; Wails picks the one that fits.
func setTrayIcon(t *application.SystemTray) { t.SetIcon(trayIcon) }

// menuText escapes & , which a Windows menu reads as an accelerator mark.
func menuText(s string) string { return strings.ReplaceAll(s, "&", "&&") }
