package diag

import "sync"

var osVersionOnce = sync.OnceValue(func() string { return osVersion() })

// OSVersion names the system's release, as far as it can be found.
func OSVersion() string { return osVersionOnce() }
