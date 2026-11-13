package main

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
int stTrayIconShown(void);
void stCloseShortcut(void);
*/
import "C"

func nativeTrayIconShown() bool { return C.stTrayIconShown() != 0 }

func nativeCloseShortcut() { C.stCloseShortcut() }
