// Command sharetoken-gui is the desktop app: a native window around the pages
// that `sharetoken daemon` serves. All the logic lives in the daemon; this
// program only finds or starts it, forwards the window's requests to its local
// API, and stops it again if it started it.
//
// It is a separate Go module so that the servers and the CLI build without cgo
// and without the webview's dependencies. See docs/desktop.md.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// The window is a small one: every view of the page fits it without scrolling
// the page, and long lists scroll inside their own panel.
const (
	windowWidth     = 880
	windowHeight    = 600
	minWindowWidth  = 760
	minWindowHeight = 520
	// screenReserve is left for the taskbar or Dock and the window's title bar.
	screenReserve = 96
)

// fittedSize is the window's size on a screen of the given size. A window taller
// than the screen would open with its title bar, and its close button, above the
// top edge: a 1080p display at 150% scaling is only 720 high.
func fittedSize(screenW, screenH int) (int, int) {
	return min(windowWidth, screenW), max(minWindowHeight, min(windowHeight, screenH-screenReserve))
}

func fitToScreen(w *application.WebviewWindow) {
	s, err := w.GetScreen()
	if err != nil || s == nil {
		return
	}
	if fw, fh := fittedSize(s.Size.Width, s.Size.Height); fw != windowWidth || fh != windowHeight {
		w.SetSize(fw, fh)
		w.Center()
	}
}

// langReportJS tells the shell which language the page shows, for the tray's
// text: once the page is there, and again whenever it changes.
const langReportJS = `(() => {
  const report = () => fetch('` + shellLangPath + `?l=' + encodeURIComponent(document.documentElement.lang));
  new MutationObserver(report).observe(document.documentElement, { attributes: true, attributeFilter: ['lang'] });
  addEventListener('DOMContentLoaded', report);
})();`

// shellClassJS tells the page which window it is in: on macOS the title bar is
// hidden and the page draws under the window's buttons, so it leaves room for
// them and marks where the window can be dragged (app.css, .os-darwin).
const shellClassJS = `document.documentElement.classList.add('os-` + goruntime.GOOS + `');`

// runtimeJS loads Wails' page runtime. It turns a mouse-down on an element
// marked --wails-draggable into a window drag (macOS, where the page draws the
// title bar), and Wails runs script in the page (ExecJS) only once it is there.
// The asset server answers /wails/runtime.js itself, so it is a script of the
// page's own origin and its CSP allows it.
const runtimeJS = `import('/wails/runtime.js').catch(e => console.error('wails runtime', e));`

// shellUpdateJS tells the page that this window installs updates by itself
// (update.go): it then offers to, instead of only linking the download.
const shellUpdateJS = `document.documentElement.dataset.shellUpdate = '1';`

// shellBootJS is the shell's script for each page the window loads (proxy.go).
func shellBootJS(selftest, canUpdate bool) string {
	js := shellClassJS + runtimeJS
	if canUpdate {
		js += shellUpdateJS
	}
	if selftest {
		return js + selftestJS
	}
	return js + langReportJS // the tray and the notifications speak the page's language
}

func errText(e *error) string {
	if e == nil {
		return "the test did not finish"
	}
	return fmt.Sprint(*e)
}

func main() {
	sup := newSupervisor()
	// The daemon is started once Wails holds the single-instance lock (taken in
	// application.New), so a second start of the app leaves without touching
	// anything. The window's first request may arrive before that, and waits.
	start := sync.OnceValue(func() error {
		err := sup.start(context.Background())
		if err != nil {
			log.Printf("sharetoken-gui: %v", err)
		}
		return err
	})

	// $SHARETOKEN_GUI_SELFTEST=1: run a request through the window's whole path
	// without showing it, report, and quit (scripts/test/gui_shell_test.sh).
	// =tray: run through the tray and the window, which shows (scripts/test/gui_tray_test.sh).
	// =update: install the offered update with the window hidden, as its button
	// would (scripts/test/gui_update_test.sh).
	mode := os.Getenv("SHARETOKEN_GUI_SELFTEST")
	trayCheck := mode == "tray"
	updateCheck := mode == "update"
	selftest := mode != "" && !trayCheck && !updateCheck
	useTray := !selftest && !updateCheck && trayAvailable()
	var trayCheckErr atomic.Pointer[error]

	result := make(chan string, 1)
	var window *application.WebviewWindow
	var app *application.App
	showWindow := func() {
		if window == nil {
			return
		}
		if goruntime.GOOS == "darwin" {
			// The app may be hidden as a whole (Command-H). Unlike the window's
			// methods, App.Show does not move itself to the main thread.
			application.InvokeSync(app.Show)
		}
		window.Show()
		window.Restore()
		window.Focus()
	}
	var nt atomic.Pointer[nativeTray]
	tray := newTrayController(start, sup.endpoint, func(m trayMenu) {
		if t := nt.Load(); t != nil {
			t.set(m)
		}
	})
	handler := newProxy(sup, start, result, tray.setLang)
	updates := newShellUpdater(sup, func() *application.App { return app }, func() { app.Quit() })
	canUpdate, whyNot := updates.supported()
	handler.update = updates
	handler.boot = shellBootJS(selftest, canUpdate)
	var updateCheckErr atomic.Pointer[error]

	// Run does not return on every platform: macOS ends the process when the app
	// terminates. So the daemon is stopped, and the self-tests report, from Wails'
	// shutdown hooks.
	stopDaemon := sync.OnceFunc(func() {
		_ = start() // one still in progress must finish before it can be stopped
		sup.stop()
	})
	finish := sync.OnceFunc(func() {
		if selftest {
			// $SHARETOKEN_GUI_SELFTEST_REPORT: also into a file, for a copy started
			// by an update, whose output goes nowhere.
			if f := os.Getenv("SHARETOKEN_GUI_SELFTEST_REPORT"); f != "" {
				_ = os.WriteFile(f, []byte(version+" "+handler.lastResult()+"\n"), 0o600)
			}
			if !strings.HasPrefix(handler.lastResult(), "ok") {
				os.Exit(1)
			}
		}
		if updateCheck {
			if e := updateCheckErr.Load(); (e == nil || *e != nil) && updates.status().State != "restarting" {
				fmt.Println("selftest: failed:", errText(e))
				os.Exit(1)
			}
			fmt.Println("selftest: ok (the update is installing)")
		}
		if trayCheck {
			if e := trayCheckErr.Load(); e == nil || *e != nil {
				fmt.Println("selftest: failed:", errText(e))
				os.Exit(1)
			}
			fmt.Println("selftest: ok")
		}
	})

	notifier := notifications.New()
	opts := application.Options{
		OnShutdown:   stopDaemon,
		PostShutdown: finish,
		Name:         windowTitle(),
		LogLevel:     slog.LevelWarn, // not Wails' build and platform lines at each start
		Assets:       application.AssetOptions{Handler: handler, DisableLogging: true},
		Services:     []application.Service{application.NewService(notifier)},
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyRegular,
			// With a tray, closing the window leaves the app and its daemon
			// running; the menu's Quit ends them.
			ApplicationShouldTerminateAfterLastWindowClosed: !useTray,
		},
		Windows: application.WindowsOptions{DisableQuitOnLastWindowClosed: useTray},
		Linux:   application.LinuxOptions{DisableQuitOnLastWindowClosed: useTray, ProgramName: daemonName()},
	}
	invites := &inviteForwarder{start: start, ep: sup.endpoint, show: showWindow}
	// One app per data directory: a second start (an invite link clicked in
	// the browser, or the app opened again) hands its arguments over and ends.
	if !selftest && !updateCheck && !trayCheck {
		opts.SingleInstance = &application.SingleInstanceOptions{
			UniqueID: singleInstanceID(sup.home),
			OnSecondInstanceLaunch: func(d application.SecondInstanceData) {
				if link := inviteArg(d.Args); link != "" {
					invites.forward(link)
					return
				}
				showWindow()
			},
		}
	} else if useTray {
		opts.SingleInstance = &application.SingleInstanceOptions{
			UniqueID:               singleInstanceID(sup.home),
			OnSecondInstanceLaunch: func(application.SecondInstanceData) { showWindow() },
		}
	}
	app = application.New(opts)
	// macOS hands a clicked link over as an Apple Event, at start or later;
	// Windows and Linux report a first start's argument this way too.
	app.Event.OnApplicationEvent(events.Common.ApplicationLaunchedWithUrl, func(e *application.ApplicationEvent) {
		if link := inviteArg([]string{e.Context().URL()}); link != "" {
			invites.forward(link)
		}
	})
	if link := inviteArg(os.Args[1:]); link != "" {
		invites.forward(link)
	}

	if goruntime.GOOS == "darwin" { // Windows and Linux windows have no menu bar; the webview handles the keys
		m := app.NewMenu()
		m.AddRole(application.AppMenu)
		if useTray { // only here is closing different from quitting
			m.AddSubmenu("File").Add("Close Window").SetAccelerator("CmdOrCtrl+W").OnClick(func(*application.Context) {
				if window != nil {
					window.Hide() // what closing does
				}
			})
		}
		m.AddRole(application.EditMenu) // without it, copy and paste do not work on macOS
		m.AddRole(application.WindowMenu)
		app.Menu.Set(m)
	}

	window = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main",
		Title:     windowTitle(),
		Width:     windowWidth,
		Height:    windowHeight,
		MinWidth:  minWindowWidth,
		MinHeight: minWindowHeight,
		URL:       "/",
		Hidden:    selftest || updateCheck,
		Mac:       application.MacWindow{TitleBar: application.MacTitleBarHiddenInset},
	})
	if useTray {
		window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
			window.Hide()
			e.Cancel()
		})
	}

	go start()
	go cleanStaging()
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		fitToScreen(window)
		ctx := app.Context()
		if useTray {
			nt.Store(startTray(app, windowTitle(), showWindow, app.Quit))
		}
		if !selftest && !updateCheck {
			if !trayCheck {
				tray.notify = (&desktopNotifier{ns: notifier, show: showWindow}).send
			}
			go tray.run(ctx)
		}
		if trayCheck {
			go func() {
				t := &trayTest{ctx: ctx, sup: sup, win: window, tray: nt.Load()}
				err := t.run(start, tray.langReported, func(lang string) {
					window.ExecJS("fetch('" + shellLangPath + "?l=" + lang + "');")
				})
				trayCheckErr.Store(&err)
				if err != nil {
					app.Quit()
					return
				}
				t.tray.click("quit")
				time.Sleep(15 * time.Second)
				e := fmt.Errorf("the menu's Quit did not end the app")
				trayCheckErr.Store(&e)
				app.Quit()
			}()
		}
		if updateCheck {
			go func() {
				err := runUpdateCheck(start, sup, updates, canUpdate, whyNot)
				updateCheckErr.Store(&err)
				if err != nil {
					app.Quit()
				}
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
				app.Quit()
			}()
		}
	})

	err := app.Run()
	if err != nil {
		stopDaemon()
		log.Fatalf("sharetoken-gui: %v", err)
	}
	finish()
}
