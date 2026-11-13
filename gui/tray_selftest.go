package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// trayProbe is what the platform reports about its tray and window.
type trayProbe struct {
	IconShown          bool // the icon is on screen with something drawn in it
	Status, Open, Quit string
	WindowVisible      bool
}

// trayTest drives the real tray: the same menu actions and window close a click
// makes, through the platform's own paths, checking the result of each
// (scripts/gui_tray_test.sh). Every step reports, so a failure names itself.
type trayTest struct {
	ctx   context.Context
	sup   *supervisor
	steps []string
}

func (t *trayTest) waitFor(what string, ok func(trayProbe) bool) error {
	deadline := time.Now().Add(10 * time.Second)
	var p trayProbe
	for time.Now().Before(deadline) {
		if p = nativeTrayProbe(); ok(p) {
			t.steps = append(t.steps, what)
			fmt.Println("tray:", what)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("%s: never happened; last seen %+v", what, p)
}

func (t *trayTest) daemonAnswers(what string) error {
	if st := fetchTrayState(t.ctx, t.sup.addr); st.Down {
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
	first := nativeTrayProbe()
	fmt.Println("tray: menu reads:", first.Status, "|", first.Open, "|", first.Quit)

	// The menu takes the language of the page: from a report by the page, and
	// from the page's own report at each load, which puts the first one back.
	const other = "th"
	show(other)
	if err := t.waitFor("menu follows the language the page reports", func(p trayProbe) bool {
		return p.Status == text(other).Status && p.Quit == text(other).Quit
	}); err != nil {
		return err
	}
	runtime.WindowReload(t.ctx)
	if err := t.waitFor("menu follows the page again after a reload", func(p trayProbe) bool { return p.Status == first.Status }); err != nil {
		return err
	}

	// Closing hides the window; the app and the daemon go on.
	nativeWindowClose()
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
	nativeTrayClick("open")
	if err := t.waitFor("Open brings the window back", visible); err != nil {
		return err
	}

	runtime.WindowMinimise(t.ctx)
	deadline := time.Now().Add(5 * time.Second)
	for !runtime.WindowIsMinimised(t.ctx) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !runtime.WindowIsMinimised(t.ctx) {
		return fmt.Errorf("the window would not minimise")
	}
	nativeTrayClick("open")
	if err := t.waitFor("Open restores a minimised window", func(p trayProbe) bool { return p.WindowVisible && !runtime.WindowIsMinimised(t.ctx) }); err != nil {
		return err
	}

	// Quickly, with no time for animations: Wails ends the program if WebView2
	// refuses the focus at the wrong moment.
	for range 5 {
		runtime.WindowMinimise(t.ctx)
		nativeTrayClick("open")
		if err := t.waitFor("minimise and Open, at once", visible); err != nil {
			return err
		}
		// Whatever a burst of clicks leaves behind, one more Open recovers it.
		runtime.WindowMinimise(t.ctx)
		time.Sleep(20 * time.Millisecond)
		nativeWindowClose()
		nativeTrayClick("open")
		time.Sleep(1500 * time.Millisecond)
		nativeTrayClick("open")
		if err := t.waitFor("minimise, close and Open, at once, then Open", func(p trayProbe) bool { return p.WindowVisible && !runtime.WindowIsMinimised(t.ctx) }); err != nil {
			return err
		}
	}
	time.Sleep(time.Second) // clicks still on their way would undo the next step

	if goruntime.GOOS == "windows" { // on macOS a click on the icon opens the menu
		nativeWindowClose()
		if err := t.waitFor("close hides the window again", hidden); err != nil {
			return err
		}
		nativeTrayClick("icon")
		if err := t.waitFor("click on the icon opens the window", visible); err != nil {
			return err
		}
	}

	if goruntime.GOOS == "darwin" { // Windows has no app menu
		nativeCloseShortcut()
		if err := t.waitFor("the Close Window shortcut hides the window", hidden); err != nil {
			return err
		}
		nativeTrayClick("open")
		if err := t.waitFor("Open brings it back after the shortcut", visible); err != nil {
			return err
		}
	}

	// A second start on this data directory only brings the window back.
	nativeWindowClose()
	if err := t.waitFor("close before the second start", hidden); err != nil {
		return err
	}
	if err := t.secondStart(); err != nil {
		return err
	}
	if err := t.waitFor("a second start brings the window back", visible); err != nil {
		return err
	}
	runtime.WindowMinimise(t.ctx)
	time.Sleep(500 * time.Millisecond)
	if err := t.secondStart(); err != nil {
		return err
	}
	if err := t.waitFor("a second start restores a minimised window", func(p trayProbe) bool { return p.WindowVisible && !runtime.WindowIsMinimised(t.ctx) }); err != nil {
		return err
	}
	return t.daemonAnswers("after the second start")
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
