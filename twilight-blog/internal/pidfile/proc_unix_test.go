//go:build unix

package pidfile

import (
	"errors"
	"syscall"
	"testing"
)

func TestProcessAliveRejectsNonPositivePID(t *testing.T) {
	if processAlive(0) {
		t.Fatal("pid 0 must not be treated as a live process")
	}
	if processAlive(-1) {
		t.Fatal("negative pid must not be treated as a live process")
	}
}

func TestProcessAliveTreatsPermissionErrorAsLive(t *testing.T) {
	original := kill
	kill = func(pid int, signal syscall.Signal) error {
		return syscall.EPERM
	}
	t.Cleanup(func() { kill = original })

	if !processAlive(1) {
		t.Fatal("EPERM means the process exists but belongs to another user; it must count as live")
	}
}

func TestProcessAliveTreatsUnknownErrorsAsLive(t *testing.T) {
	original := kill
	want := errors.New("unexpected probe failure")
	kill = func(pid int, signal syscall.Signal) error {
		return want
	}
	t.Cleanup(func() { kill = original })

	if !processAlive(1) {
		t.Fatal("an unknown probe error must fail closed and count as live")
	}
}
