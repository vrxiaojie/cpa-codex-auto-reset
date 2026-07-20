//go:build !windows

package state

import (
	"fmt"
	"os"
)

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}

func syncDirectory(dir string) error {
	handle, errOpen := os.Open(dir)
	if errOpen != nil {
		return fmt.Errorf("open state directory for sync: %w", errOpen)
	}
	defer handle.Close()
	if errSync := handle.Sync(); errSync != nil {
		return fmt.Errorf("sync state directory: %w", errSync)
	}
	return nil
}
