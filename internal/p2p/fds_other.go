//go:build !unix

package p2p

import "math"

// numFDs: libp2p does not limit descriptors where it cannot read a limit.
func numFDs() int { return math.MaxInt }
