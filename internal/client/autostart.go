package client

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"sharetoken/internal/diag"
)

// Autostart runs the client's daemon at login, restarted when it fails:
// with the tools connected (sharetoken connect), Claude Code and Codex reach
// even their own vendor's models through it, so it has to be there when
// they are. The desktop app uses the daemon it finds running.
//
//	macOS    a LaunchAgent (~/Library/LaunchAgents/<label>.plist)
//	Linux    a systemd user service (~/.config/systemd/user/<name>.service)
//	Windows  a scheduled task at logon

func autostartName() string {
	if Env == EnvProd {
		return "sharetoken"
	}
	return "sharetoken-" + Env
}

func autostartLabel() string {
	if Env == EnvProd {
		return "app.sharetoken.client"
	}
	return "app.sharetoken.client." + Env
}

// AppAutostartOn reports whether the desktop app starts at login (its own
// entry, gui/autostart*.go), which runs the client as well.
func AppAutostartOn() bool {
	h, _ := os.UserHomeDir()
	suffix := ""
	if Env != EnvProd {
		suffix = "-" + Env
	}
	switch runtime.GOOS {
	case "darwin":
		label := "app.sharetoken.desktop"
		if Env != EnvProd {
			label += "." + Env
		}
		_, err := os.Stat(filepath.Join(h, "Library", "LaunchAgents", label+".plist"))
		return err == nil
	case "linux":
		dir := os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			dir = filepath.Join(h, ".config")
		}
		_, err := os.Stat(filepath.Join(dir, "autostart", "sharetoken"+suffix+".desktop"))
		return err == nil
	case "windows":
		return exec.Command("reg", "query", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", "ShareToken"+suffix).Run() == nil
	}
	return false
}

// AutostartStatus says whether autostart is set up, and where.
func AutostartStatus() (on bool, where string) {
	h, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		p := filepath.Join(h, "Library", "LaunchAgents", autostartLabel()+".plist")
		_, err := os.Stat(p)
		return err == nil, p
	case "linux":
		p := filepath.Join(h, ".config", "systemd", "user", autostartName()+".service")
		_, err := os.Stat(p)
		return err == nil, p
	case "windows":
		err := exec.Command("schtasks", "/Query", "/TN", autostartTask()).Run()
		return err == nil, "task " + autostartTask()
	}
	return false, ""
}

func autostartTask() string {
	if Env == EnvProd {
		return "ShareToken"
	}
	return "ShareToken-" + Env
}

// SetAutostart sets autostart up for this program with home, or removes it.
func SetAutostart(home string, on bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	h, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		p := filepath.Join(h, "Library", "LaunchAgents", autostartLabel()+".plist")
		domain := "gui/" + strconv.Itoa(os.Getuid())
		_ = exec.Command("launchctl", "bootout", domain+"/"+autostartLabel()).Run()
		if !on {
			return removeIfThere(p)
		}
		// The client writes its own log (diag.go); this file only gets what
		// it writes before that is open, and the runtime's own output.
		cfg, _ := LoadConfig(home)
		logp := filepath.Join(LogDir(home, "", cfg), diag.StderrLog)
		if err := os.MkdirAll(filepath.Dir(logp), 0o700); err != nil {
			return err
		}
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>-home</string><string>%s</string><string>daemon</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, xmlEscape(autostartLabel()), xmlEscape(exe), xmlEscape(home), xmlEscape(logp), xmlEscape(logp))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
			return err
		}
		return runQuiet("launchctl", "bootstrap", domain, p)
	case "linux":
		p := filepath.Join(h, ".config", "systemd", "user", autostartName()+".service")
		if !on {
			_ = exec.Command("systemctl", "--user", "disable", "--now", autostartName()).Run()
			return removeIfThere(p)
		}
		unit := fmt.Sprintf("[Unit]\nDescription=ShareToken client\nAfter=network-online.target\n\n[Service]\nExecStart=%s -home %s daemon\nRestart=on-failure\nRestartSec=10\n\n[Install]\nWantedBy=default.target\n",
			systemdQuote(exe), systemdQuote(home))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(unit), 0o644); err != nil {
			return err
		}
		if err := runQuiet("systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		return runQuiet("systemctl", "--user", "enable", "--now", autostartName())
	case "windows":
		_ = exec.Command("schtasks", "/End", "/TN", autostartTask()).Run()
		_ = exec.Command("schtasks", "/Delete", "/TN", autostartTask(), "/F").Run()
		if !on {
			return nil
		}
		// Through a hidden PowerShell: the client is a console program, and a
		// task that ran it would open a console window at each logon.
		tr := fmt.Sprintf(`powershell.exe -NoProfile -WindowStyle Hidden -Command "& '%s' -home '%s' daemon"`, psQuote(exe), psQuote(home))
		if err := runQuiet("schtasks", "/Create", "/TN", autostartTask(), "/SC", "ONLOGON", "/RL", "LIMITED", "/TR", tr, "/F"); err != nil {
			return err
		}
		return runQuiet("schtasks", "/Run", "/TN", autostartTask())
	}
	return errors.New("autostart is not supported on " + runtime.GOOS)
}

// psQuote escapes s for a single-quoted PowerShell string.
func psQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

func removeIfThere(p string) error {
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func runQuiet(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&"+"amp;", "<", "&"+"lt;", ">", "&"+"gt;", "\"", "&"+"quot;")
	return r.Replace(s)
}

func systemdQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"\\") {
		return s
	}
	return strconv.Quote(s)
}
