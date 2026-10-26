//go:build windows

package qoderhook

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockQoderFile(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	overlapped := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
	if err != nil {
		file.Close()
		return nil, err
	}
	return func() { windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped); file.Close() }, nil
}
