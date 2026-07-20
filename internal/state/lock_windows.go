//go:build windows

package state

import (
	"os"
	"syscall"
	"unsafe"
)

type fileLock struct {
	file       *os.File
	overlapped syscall.Overlapped
}

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2
)

var (
	lockFileEx   = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")
	unlockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("UnlockFileEx")
)

func acquireFileLock(path string) (*fileLock, error) {
	file, errOpen := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if errOpen != nil {
		return nil, errOpen
	}
	lock := &fileLock{file: file}
	result, _, errCall := lockFileEx.Call(
		file.Fd(),
		uintptr(lockfileFailImmediately|lockfileExclusiveLock),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&lock.overlapped)),
	)
	if result == 0 {
		_ = file.Close()
		return nil, errCall
	}
	return lock, nil
}

func (l *fileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_, _, _ = unlockFileEx.Call(l.file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&l.overlapped)))
	return l.file.Close()
}
