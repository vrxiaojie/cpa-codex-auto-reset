//go:build windows

package state

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

var moveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func replaceFile(source, destination string) error {
	sourcePtr, errSource := syscall.UTF16PtrFromString(source)
	if errSource != nil {
		return errSource
	}
	destinationPtr, errDestination := syscall.UTF16PtrFromString(destination)
	if errDestination != nil {
		return errDestination
	}
	result, _, errCall := moveFileExW.Call(
		uintptr(unsafe.Pointer(sourcePtr)),
		uintptr(unsafe.Pointer(destinationPtr)),
		uintptr(moveFileReplaceExisting|moveFileWriteThrough),
	)
	if result == 0 {
		return fmt.Errorf("MoveFileExW: %w", errCall)
	}
	return nil
}

func syncDirectory(string) error {
	// MoveFileExW with WRITE_THROUGH flushes the replacement before returning.
	return nil
}
