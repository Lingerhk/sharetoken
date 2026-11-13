package main

/*
#include <stdlib.h>
typedef struct { int iconShown, windowVisible; char *status, *open, *quit; } stTrayState;
stTrayState stTrayProbe(void);
void stTrayClick(int open);
void stWindowClose(void);
void stCloseShortcut(void);
*/
import "C"

import "unsafe"

func nativeTrayProbe() trayProbe {
	s := C.stTrayProbe()
	defer C.free(unsafe.Pointer(s.status))
	defer C.free(unsafe.Pointer(s.open))
	defer C.free(unsafe.Pointer(s.quit))
	return trayProbe{IconShown: s.iconShown != 0, WindowVisible: s.windowVisible != 0,
		Status: C.GoString(s.status), Open: C.GoString(s.open), Quit: C.GoString(s.quit)}
}

func nativeTrayClick(what string) {
	open := C.int(0)
	if what == "open" {
		open = 1
	}
	C.stTrayClick(open)
}

func nativeWindowClose() { C.stWindowClose() }

func nativeCloseShortcut() { C.stCloseShortcut() }
