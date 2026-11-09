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
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
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

// langReportJS tells the shell which language the page chose, for the tray's text.
const langReportJS = `fetch('` + shellLangPath + `?l=' + encodeURIComponent(document.documentElement.lang));`

func errText(e *error) string {
	if e == nil {
		return "the test did not finish"
	}
	return fmt.Sprint(*e)
}

func main() {
	sup := newSupervisor()
	// The daemon is started once Wails holds the single-instance lock (OnStartup),
	// so a second start of the app leaves without touching anything. The window's
	// first request may arrive before that, and waits.
	start := sync.OnceValue(func() error {
		err := sup.start(context.Background())
		if err != nil {
			log.Printf("sharetoken-gui: %v", err)
		}
		return err
	})

	// $SHARETOKEN_GUI_SELFTEST=1: run a request through the window's whole path
	// without showing it, report, and quit (scripts/gui_shell_test.sh).
	// =tray: run through the tray and the window, which shows (scripts/gui_tray_test.sh).
	mode := os.Getenv("SHARETOKEN_GUI_SELFTEST")
	trayCheck := mode == "tray"
	selftest := mode != "" && !trayCheck
	useTray := trayAvailable && !selftest
	var trayCheckErr atomic.Pointer[error]

	result := make(chan string, 1)
	var appCtx atomic.Pointer[context.Context]
	showWindow := func() {
		if ctx := appCtx.Load(); ctx != nil {
			nativeShowWindow(*ctx)
		}
	}
	tray := newTrayController(start, func() string { return sup.addr }, nativeTraySet)
	handler := newProxy(sup, start, result, tray.setLang)

	var m *menu.Menu
	if goruntime.GOOS != "windows" { // Wails draws these roles as blank menus on Windows, where WebView2 handles the keys itself
		m = menu.NewMenu()
		m.Append(menu.AppMenu())
		if useTray { // only here is closing different from quitting
			m.AddSubmenu("File").AddText("Close Window", keys.CmdOrCtrl("w"), func(*menu.CallbackData) {
				if ctx := appCtx.Load(); ctx != nil {
					runtime.Hide(*ctx) // what closing does
				}
			})
		}
		m.Append(menu.EditMenu()) // without it, copy and paste do not work on macOS
		m.Append(menu.WindowMenu())
	}

	app := &options.App{
		Title:     windowTitle(),
		Width:     windowWidth,
		Height:    windowHeight,
		MinWidth:  480,
		MinHeight: minWindowHeight,
		Menu:      m,
		// With a tray, closing the window leaves the app and its daemon running;
		// the menu's Quit ends them.
		HideWindowOnClose: useTray,
		OnBeforeClose:     onBeforeClose,
		StartHidden:       selftest,
		AssetServer:       &assetserver.Options{Handler: handler},
		OnDomReady: func(ctx context.Context) {
			if selftest {
				runtime.WindowExecJS(ctx, selftestJS)
			}
			if useTray {
				runtime.WindowExecJS(ctx, langReportJS)
			}
		},
		OnStartup: func(ctx context.Context) {
			appCtx.Store(&ctx)
			fitToScreen(ctx)
			go start()
			if useTray {
				nativeTrayStart(windowTitle(), showWindow, func() { runtime.Quit(ctx) })
				go tray.run(ctx)
			}
			if trayCheck {
				go func() {
					t := &trayTest{ctx: ctx, sup: sup}
					err := t.run(start, tray.langReported, func(lang string) {
						runtime.WindowExecJS(ctx, "fetch('"+shellLangPath+"?l="+lang+"');")
					})
					trayCheckErr.Store(&err)
					if err != nil {
						runtime.Quit(ctx)
						return
					}
					nativeTrayClick("quit")
					time.Sleep(15 * time.Second)
					e := fmt.Errorf("the menu's Quit did not end the app")
					trayCheckErr.Store(&e)
					runtime.Quit(ctx)
				}()
			}
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
	}
	if useTray {
		app.SingleInstanceLock = &options.SingleInstanceLock{
			UniqueId:               singleInstanceID(sup.home),
			OnSecondInstanceLaunch: func(options.SecondInstanceData) { showWindow() },
		}
	}
	err := wails.Run(app)
	if useTray {
		nativeTrayStop()
	}
	_ = start() // one still in progress must finish before it can be stopped
	sup.stop()
	if err != nil {
		log.Fatalf("sharetoken-gui: %v", err)
	}
	if selftest && !strings.HasPrefix(handler.lastResult(), "ok") {
		os.Exit(1)
	}
	if trayCheck {
		if e := trayCheckErr.Load(); e == nil || *e != nil {
			fmt.Println("selftest: failed:", errText(e))
			os.Exit(1)
		}
		fmt.Println("selftest: ok")
	}
}
