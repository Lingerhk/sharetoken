//go:build !darwin && !windows

package main

import "context"

// Linux has no tray here: closing the window quits, as before.
const trayAvailable = false

func nativeTrayStart(tooltip string, onOpen, onQuit func()) {}
func nativeTraySet(m trayMenu)                              {}
func nativeTrayStop()                                       {}
func nativeShowWindow(context.Context)                      {}
