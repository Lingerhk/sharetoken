package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// trayProbe is what the platform reports about its tray and window.
type trayProbe struct {
	IconShown          bool // the icon is on screen with something drawn in it
	Status, Open, Quit string
	WindowVisible      bool
}

// trayTest drives the real app: the menu items' actions, the window's own close,
// minimise and reload, a click on the icon (Windows) and Command-W (macOS)
// through the platform's messages, and a second start of the program, checking
// the result of each (scripts/gui_tray_test.sh). Whether the icon is on screen
// is asked of the platform. Every step reports, so a failure names itself.
type trayTest struct {
	ctx   context.Context
	sup   *supervisor
	win   *application.WebviewWindow
	tray  *nativeTray
	steps []string
}

// probe reads the tray's menu as last set, whether the platform shows its icon,
// and the window's state.
func (t *trayTest) probe() trayProbe {
	m := t.tray.shownMenu()
	return trayProbe{IconShown: nativeTrayIconShown(), Status: m.Status, Open: m.Open, Quit: m.Quit,
		WindowVisible: windowShown(t.win)}
}

func (t *trayTest) waitFor(what string, ok func(trayProbe) bool) error {
	deadline := time.Now().Add(10 * time.Second)
	var p trayProbe
	for time.Now().Before(deadline) {
		if p = t.probe(); ok(p) {
			t.steps = append(t.steps, what)
			fmt.Println("tray:", what)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("%s: never happened; last seen %+v", what, p)
}

func (t *trayTest) daemonAnswers(what string) error {
	if st := fetchTrayState(t.ctx, t.sup.endpoint()); st.Down {
		return fmt.Errorf("%s: the daemon does not answer", what)
	}
	return nil
}

func (t *trayTest) run(start func() error, reported func() bool, show func(lang string)) error {
	if err := start(); err != nil {
		return err
	}
	if err := t.waitFor("the page reports its language", func(trayProbe) bool { return reported() }); err != nil {
		return err
	}
	text := func(lang string) trayMenu {
		return renderTray(trayTexts[lang], trayState{State: stateLoggedOut})
	}
	visible := func(p trayProbe) bool { return p.WindowVisible }
	hidden := func(p trayProbe) bool { return !p.WindowVisible }
	if err := t.waitFor("icon on screen, status line signed out, window showing", func(p trayProbe) bool {
		if !p.IconShown || !p.WindowVisible {
			return false
		}
		for lang := range trayTexts {
			if m := text(lang); p.Status == m.Status && p.Open == m.Open && p.Quit == m.Quit {
				return true
			}
		}
		return false
	}); err != nil {
		return err
	}
	first := t.probe()
	fmt.Println("tray: menu reads:", first.Status, "|", first.Open, "|", first.Quit)

	// The menu takes the language of the page: from a report by the page, and
	// from the page's own report at each load, which puts the first one back.
	other := "zh-CN" // the other of the two languages the tray speaks
	if text("zh-CN").Status == first.Status {
		other = "en"
	}
	show(other)
	if err := t.waitFor("menu follows the language the page reports", func(p trayProbe) bool {
		return p.Status == text(other).Status && p.Quit == text(other).Quit
	}); err != nil {
		return err
	}
	t.win.Reload()
	if err := t.waitFor("menu follows the page again after a reload", func(p trayProbe) bool { return p.Status == first.Status }); err != nil {
		return err
	}

	// Closing hides the window; the app and the daemon go on.
	t.win.Close()
	if err := t.waitFor("close hides the window", hidden); err != nil {
		return err
	}
	if err := t.daemonAnswers("after close"); err != nil {
		return err
	}
	time.Sleep(trayPollEvery + time.Second) // through a poll of the status line while closed
	if err := t.waitFor("tray still there while the window is closed", func(p trayProbe) bool { return p.IconShown && !p.WindowVisible && p.Status == first.Status }); err != nil {
		return err
	}
	t.tray.click("open")
	if err := t.waitFor("Open brings the window back", visible); err != nil {
		return err
	}

	if minimiseObservable() {
		t.win.Minimise()
		deadline := time.Now().Add(5 * time.Second)
		for !t.win.IsMinimised() && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if !t.win.IsMinimised() {
			return fmt.Errorf("the window would not minimise")
		}
		t.tray.click("open")
		if err := t.waitFor("Open restores a minimised window", func(p trayProbe) bool { return p.WindowVisible && !t.win.IsMinimised() }); err != nil {
			return err
		}

		// Quickly, with no time for animations: Wails ends the program if WebView2
		// refuses the focus at the wrong moment.
		for range 5 {
			t.win.Minimise()
			t.tray.click("open")
			if err := t.waitFor("minimise and Open, at once", visible); err != nil {
				return err
			}
			// Whatever a burst of clicks leaves behind, one more Open recovers it.
			t.win.Minimise()
			time.Sleep(20 * time.Millisecond)
			t.win.Close()
			t.tray.click("open")
			time.Sleep(1500 * time.Millisecond)
			t.tray.click("open")
			if err := t.waitFor("minimise, close and Open, at once, then Open", func(p trayProbe) bool { return p.WindowVisible && !t.win.IsMinimised() }); err != nil {
				return err
			}
		}
		time.Sleep(time.Second) // clicks still on their way would undo the next step
	} else {
		fmt.Println("tray:", minimiseSkipped)
	}

	if goruntime.GOOS == "windows" { // on macOS a click on the icon opens the menu
		t.win.Close()
		if err := t.waitFor("close hides the window again", hidden); err != nil {
			return err
		}
		t.tray.click("icon")
		if err := t.waitFor("click on the icon opens the window", visible); err != nil {
			return err
		}
	}

	if goruntime.GOOS == "darwin" { // Windows has no app menu
		nativeCloseShortcut()
		if err := t.waitFor("the Close Window shortcut hides the window", hidden); err != nil {
			return err
		}
		t.tray.click("open")
		if err := t.waitFor("Open brings it back after the shortcut", visible); err != nil {
			return err
		}
	}

	// A second start on this data directory only brings the window back.
	t.win.Close()
	if err := t.waitFor("close before the second start", hidden); err != nil {
		return err
	}
	if err := t.secondStart(); err != nil {
		return err
	}
	if err := t.waitFor("a second start brings the window back", visible); err != nil {
		return err
	}
	if minimiseObservable() {
		t.win.Minimise()
		time.Sleep(500 * time.Millisecond)
		if err := t.secondStart(); err != nil {
			return err
		}
		if err := t.waitFor("a second start restores a minimised window", func(p trayProbe) bool { return p.WindowVisible && !t.win.IsMinimised() }); err != nil {
			return err
		}
	}
	return t.daemonAnswers("after the second start")
}

// minimiseSkipped is what the test says where it cannot check minimising.
const minimiseSkipped = "Wayland does not tell an app that its window is minimised: the minimise steps are skipped (GDK_BACKEND=x11 runs them)"

// minimiseObservable: the app can tell that its window is minimised. A GTK
// window on Wayland cannot: the compositor does not report it.
func minimiseObservable() bool {
	return goruntime.GOOS != "linux" || os.Getenv("WAYLAND_DISPLAY") == "" || os.Getenv("GDK_BACKEND") == "x11"
}

// secondStart runs this program again, as a user would, and it must leave by
// itself. It runs without the test setting: it is the ordinary app.
func (t *trayTest) secondStart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SHARETOKEN_GUI_SELFTEST=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("the second start: %w", err)
		}
		return nil
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		return fmt.Errorf("the second start did not leave")
	}
}
