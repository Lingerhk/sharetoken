package main

/*
#cgo pkg-config: gtk+-3.0
#include <gtk/gtk.h>

static gboolean stQuitMain(gpointer unused) {
	gtk_main_quit();
	return G_SOURCE_REMOVE;
}

static void stQuitOnMainThread(void) {
	g_idle_add(stQuitMain, NULL);
}
*/
import "C"

import "context"

// quitOnGTKThread replaces how Wails ends the program on Linux. For both a closed
// window and runtime.Quit it calls gtk_main_quit on a goroutine, but GTK is not
// thread-safe: now and then the call is lost, and the window is gone while the
// process, and with it the daemon, stays. Returning true stops Wails' own call,
// and the idle callback makes the same call on GTK's thread.
var onBeforeClose = func(context.Context) bool {
	C.stQuitOnMainThread()
	return true
}
