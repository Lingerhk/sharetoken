package diag

import (
	"os"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procGetUserDefaultGeoName = kernel32.NewProc("GetUserDefaultGeoName")
	procGetUserDefaultLocale  = kernel32.NewProc("GetUserDefaultLocaleName")
)

// systemCountry is the "Country or region" of Windows' settings, else the
// country of the user's locale.
func systemCountry() string {
	buf := make([]uint16, 16)
	if procGetUserDefaultGeoName.Find() == nil {
		if n, _, _ := procGetUserDefaultGeoName.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))); n > 1 {
			if c := syscall.UTF16ToString(buf); len(c) == 2 {
				return strings.ToUpper(c)
			}
		}
	}
	buf = make([]uint16, 85)
	if procGetUserDefaultLocale.Find() == nil {
		if n, _, _ := procGetUserDefaultLocale.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))); n > 1 {
			if c := countryOfLocale(syscall.UTF16ToString(buf)); c != "" {
				return c
			}
		}
	}
	return envCountry()
}

// systemZone: Windows names its zones its own way (W. Europe Standard Time
// covers non-EEA countries too), so only $TZ counts; the country decides.
func systemZone() string { return strings.TrimPrefix(os.Getenv("TZ"), ":") }
