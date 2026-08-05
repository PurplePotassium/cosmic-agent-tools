//go:build windows

package recycle

import (
	"fmt"
	"syscall"
	"unsafe"
)

// dispose sends abs to the Windows Recycle Bin via SHFileOperationW with
// FOF_ALLOWUNDO — the same operation Explorer's Delete performs, no shelling
// out. IFileOperation is the modern API, but the classic one is a single
// flat call and remains fully supported.
func dispose(abs string) error {
	const (
		foDelete          = 3
		fofAllowUndo      = 0x0040
		fofNoConfirmation = 0x0010
		fofSilent         = 0x0004
		fofNoErrorUI      = 0x0400
	)
	// pFrom is a double-NUL-terminated list of NUL-terminated paths.
	from, err := syscall.UTF16FromString(abs)
	if err != nil {
		return fmt.Errorf("recycle: %w", err)
	}
	from = append(from, 0)

	// SHFILEOPSTRUCTW (x64: natural alignment, matching this layout).
	type shFileOp struct {
		hwnd                  uintptr
		wFunc                 uint32
		pFrom                 *uint16
		pTo                   *uint16
		fFlags                uint16
		fAnyOperationsAborted int32
		hNameMappings         uintptr
		lpszProgressTitle     *uint16
	}
	op := shFileOp{
		wFunc:  foDelete,
		pFrom:  &from[0],
		fFlags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI,
	}
	shell32 := syscall.NewLazyDLL("shell32.dll")
	proc := shell32.NewProc("SHFileOperationW")
	ret, _, callErr := proc.Call(uintptr(unsafe.Pointer(&op)))
	if ret != 0 {
		return fmt.Errorf("recycle: SHFileOperationW failed (code %#x)", ret)
	}
	if op.fAnyOperationsAborted != 0 {
		return fmt.Errorf("recycle: operation aborted")
	}
	_ = callErr // syscall.Errno(0) on success; ret is the authoritative result
	return nil
}
