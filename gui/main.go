// Command sharetoken-gui is the desktop app: a native window around the pages
// that `sharetoken daemon` serves. All the logic lives in the daemon; this
// program only finds or starts it, forwards the window's requests to its local
// API, and stops it again if it started it.
//
// It is a separate Go module so that the servers and the CLI build without cgo
// and without the webview's dependencies. See docs/gui.md.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	windowWidth     = 1040
	windowHeight    = 760
	minWindowHeight = 560
	// screenReserve is left for the taskbar or Dock and the window's title bar.
	screenReserve = 96
)

// fittedSize is the window's size on a screen of the given size. A window taller
// than the screen would open with its title bar, and its close button, above the
// top edge: a 1080p display at 150% scaling is only 720 high.
func fittedSize(screenW, screenH int) (int, int) {
	return min(windowWidth, screenW), max(minWindowHeight, min(windowHeight, screenH-screenReserve))
}

func fitToScreen(ctx context.Context) {
	screens, err := runtime.ScreenGetAll(ctx)
	if err != nil {
		return
	}
	for _, s := range screens {
		if !s.IsCurrent {
			continue
		}
		if w, h := fittedSize(s.Size.Width, s.Size.Height); w != windowWidth || h != windowHeight {
			runtime.WindowSetSize(ctx, w, h)
			runtime.WindowCenter(ctx)
		}
		return
	}
}

func main() {
	sup := newSupervisor()
	startErr := sup.start(context.Background())
	if startErr != nil {
		log.Printf("sharetoken-gui: %v", startErr)
	}
	defer sup.stop()

	// $SHARETOKEN_GUI_SELFTEST=1: run a request through the window's whole path
	// without showing it, report, and quit (scripts/gui_shell_test.sh).
	selftest := os.Getenv("SHARETOKEN_GUI_SELFTEST") != ""
	result := make(chan string, 1)
	handler := newProxy(sup, startErr, result)

	var m *menu.Menu
	if goruntime.GOOS != "windows" { // Wails draws these roles as blank menus on Windows, where WebView2 handles the keys itself
		m = menu.NewMenu()
		m.Append(menu.AppMenu())
		m.Append(menu.EditMenu()) // without it, copy and paste do not work on macOS
	}

	err := wails.Run(&options.App{
		Title:       windowTitle(),
		Width:       windowWidth,
		Height:      windowHeight,
		MinWidth:    480,
		MinHeight:   minWindowHeight,
		Menu:        m,
		StartHidden: selftest,
		AssetServer: &assetserver.Options{Handler: handler},
		OnDomReady: func(ctx context.Context) {
			if selftest {
				runtime.WindowExecJS(ctx, selftestJS)
			}
		},
		OnStartup: func(ctx context.Context) {
			fitToScreen(ctx)
			if selftest {
				go func() {
					select {
					case r := <-result:
						fmt.Println("selftest:", r)
					case <-time.After(60 * time.Second):
						fmt.Println("selftest: failed: the page never reported")
					}
					runtime.Quit(ctx)
				}()
			}
		},
	})
	if err != nil {
		sup.stop()
		log.Fatalf("sharetoken-gui: %v", err)
	}
	if selftest && !strings.HasPrefix(handler.lastResult(), "ok") {
		sup.stop()
		os.Exit(1)
	}
}
