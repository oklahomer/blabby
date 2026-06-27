package workerlease

import (
	"fmt"
	"os"
)

// HostPIDOwner builds a best-effort "host/pid" string identifying the calling
// process in the worker_lease table for observability. It is not load-bearing for
// correctness — the per-lease fencing token, not the owner, is what keeps two
// processes from sharing a worker id — so an unavailable hostname degrades to
// "unknown" rather than failing. Both id-minting tiers (backend and gateway) use
// it so their lease rows are labelled consistently.
func HostPIDOwner() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return fmt.Sprintf("%s/%d", host, os.Getpid())
}
