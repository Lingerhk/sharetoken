//go:build !darwin && !windows

package main

func nativeTrayProbe() trayProbe  { return trayProbe{} }
func nativeTrayClick(what string) {}
func nativeWindowClose()          {}
func nativeCloseShortcut()        {}
