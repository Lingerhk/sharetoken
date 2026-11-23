package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func autostartDesktopFile() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		h, _ := os.UserHomeDir()
		dir = filepath.Join(h, ".config")
	}
	return filepath.Join(dir, "autostart", strings.ToLower(autostartName())+".desktop")
}

func appAutostart() (bool, string) {
	p := autostartDesktopFile()
	_, err := os.Stat(p)
	return err == nil, p
}

// setAppAutostart writes or removes an XDG autostart entry. GNOME starts it
// 10 seconds after login: started at once, the app came up before the
// AppIndicator host, and showed its window with no tray icon (tested on
// Ubuntu 24.04).
func setAppAutostart(on bool) error {
	p := autostartDesktopFile()
	if !on {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	exe, err := appExecutable()
	if err != nil {
		return err
	}
	quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", `\$`).Replace(exe) + `"`
	entry := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=%s\nExec=%s %s\nIcon=%s\nTerminal=false\nX-GNOME-Autostart-enabled=true\nX-GNOME-Autostart-Delay=10\nNoDisplay=true\n",
		autostartName(), quoted, hiddenArg, strings.TrimSuffix(daemonName(), ".exe"))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(entry), 0o644)
}
