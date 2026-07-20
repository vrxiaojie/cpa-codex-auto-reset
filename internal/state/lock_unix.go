//go:build !windows

package state

import (
	"fmt"
	"os"
	"syscall"
)

type fileLock struct {
	file *os.File
}

func acquireFileLock(path string) (*fileLock, error) {
	file, errOpen := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if errOpen != nil {
		return nil, errOpen
	}
	if errFlock := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); errFlock != nil {
		_ = file.Close()
		return nil, fmt.Errorf("state is locked by another process: %w", errFlock)
	}
	return &fileLock{file: file}, nil
}

func (l *fileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}
