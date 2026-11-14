package p2p

import (
	"bufio"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/pbnjay/memory"
)

// machineMemory is the memory this process may use: the machine's, or less
// when a cgroup (v2) limits it, as systemd units and containers do. The
// resource manager's limits scale with it; on a small relay a cgroup limit
// that libp2p does not see would let peers reserve more than the kernel
// allows before it kills the process.
func machineMemory() uint64 {
	total := memory.TotalMemory()
	if lim := cgroupMemoryMax(); lim > 0 && (total == 0 || lim < total) {
		return lim
	}
	return total
}

// cgroupMemoryMax is the smallest memory.max on the path from this process's
// cgroup to the root, or 0 when there is none (no cgroup v2, not Linux).
func cgroupMemoryMax() uint64 {
	f, err := os.Open("/proc/self/cgroup")
	if err != nil {
		return 0
	}
	defer f.Close()
	var dir string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if p, ok := strings.CutPrefix(sc.Text(), "0::"); ok {
			dir = p
		}
	}
	if dir == "" {
		return 0
	}
	var lim uint64
	for d := dir; ; d = path.Dir(d) {
		if b, err := os.ReadFile(path.Join("/sys/fs/cgroup", d, "memory.max")); err == nil {
			if v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64); err == nil && (lim == 0 || v < lim) {
				lim = v
			}
		}
		if d == "/" || d == "." {
			return lim
		}
	}
}
