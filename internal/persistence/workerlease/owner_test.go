package workerlease

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestHostPIDOwner(t *testing.T) {
	got := HostPIDOwner()
	host, pid, ok := strings.Cut(got, "/")
	if !ok || host == "" {
		t.Fatalf("HostPIDOwner() = %q, want a non-empty host/pid", got)
	}
	if pid != strconv.Itoa(os.Getpid()) {
		t.Errorf("pid segment = %q, want %d", pid, os.Getpid())
	}
}
