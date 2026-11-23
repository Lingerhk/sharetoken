package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func autostartLabel() string {
	if Env == envProd {
		return "app.sharetoken.desktop"
	}
	return "app.sharetoken.desktop." + Env
}

func autostartPlist() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Library", "LaunchAgents", autostartLabel()+".plist")
}

func appAutostart() (bool, string) {
	p := autostartPlist()
	_, err := os.Stat(p)
	return err == nil, p
}

// setAppAutostart writes or removes the LaunchAgent. It opens the bundle
// through Launch Services (open -g: in the background), as a login item
// would; a binary outside a bundle (a development build) is run directly.
func setAppAutostart(on bool) error {
	p := autostartPlist()
	domain := "gui/" + strconv.Itoa(os.Getuid())
	if !on {
		_ = exec.Command("launchctl", "bootout", domain+"/"+autostartLabel()).Run()
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	exe, err := appExecutable()
	if err != nil {
		return err
	}
	args := []string{exe, hiddenArg}
	if i := strings.Index(exe, ".app/Contents/MacOS/"); i >= 0 {
		args = []string{"/usr/bin/open", "-g", "-a", exe[:i+len(".app")], "--args", hiddenArg}
	}
	var b strings.Builder
	for _, a := range args {
		fmt.Fprintf(&b, "<string>%s</string>", xmlEscape(a))
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>%s</array>
  <key>RunAtLoad</key><true/>
  <key>LimitLoadToSessionType</key><string>Aqua</string>
</dict>
</plist>
`, xmlEscape(autostartLabel()), b.String())
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// Loaded at the next login; not now: the app is running already.
	return os.WriteFile(p, []byte(plist), 0o644)
}
