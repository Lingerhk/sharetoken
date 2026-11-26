package main

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func appAutostart() (bool, string) {
	where := `HKCU\` + runKey + `\` + autostartName()
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false, where
	}
	defer k.Close()
	_, _, err = k.GetStringValue(autostartName())
	return err == nil, where
}

// setAppAutostart sets or removes the app's value under the Run key.
func setAppAutostart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(autostartName()); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, err := appExecutable()
	if err != nil {
		return err
	}
	return k.SetStringValue(autostartName(), `"`+exe+`" `+hiddenArg)
}
