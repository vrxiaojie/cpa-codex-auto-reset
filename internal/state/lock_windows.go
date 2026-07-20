//go:build windows

package state

import (
	"os"
)

type fileLock struct {
	file *os.File
	path string
}

func acquireFileLock(path string) (*fileLock, error) {
	file, errOpen := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if errOpen != nil {
		return nil, errOpen
	}
	return &fileLock{file: file, path: path}, nil
}

func (l *fileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	errClose := l.file.Close()
	if errRemove := os.Remove(l.path); errClose == nil {
		return errRemove
	}
	return errClose
}
