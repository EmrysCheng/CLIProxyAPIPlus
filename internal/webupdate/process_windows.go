//go:build windows

package webupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func hideProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP}
}
func waitForParent(ctx context.Context, pid int) error {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		state, e := windows.WaitForSingleObject(h, 100)
		if e != nil {
			return e
		}
		if state == windows.WAIT_OBJECT_0 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Windows can retain a just-exited executable briefly (image teardown or a
// security scanner). Retry only sharing/permission failures for a bounded time;
// permanent ACL failures still abort and enter paired recovery.
func renameFile(source, destination string) error {
	deadline := time.Now().Add(10 * time.Second)
	var err error
	for {
		if err = noLinks(source); err != nil {
			return err
		}
		if err = noLinks(destination); err != nil {
			return err
		}
		err = renameOnce(source, destination)
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// openRead shares delete access so polling a journal never blocks its atomic
// replacement. The path is checked for links by the caller before opening.
func openRead(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

func trustedOwner(path string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	if sd == nil {
		return errors.New("missing update-directory security descriptor")
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sid := owner.String()
	if sid != user.User.Sid.String() && sid != "S-1-5-18" && sid != "S-1-5-32-544" {
		return errors.New("update directory is owned by another untrusted account")
	}
	return nil
}

// FILE_RENAME_INFO uses a flexible UTF-16 tail. POSIX semantics keep an old
// reader's handle valid while subsequent opens observe the replacement file.
func renameOnce(source, destination string) error {
	sourceName, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(sourceName, windows.DELETE|windows.SYNCHRONIZE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	name, err := windows.UTF16FromString(absolute)
	if err != nil {
		return err
	}
	type renameInfo struct {
		Flags  uint32
		Root   windows.Handle
		Length uint32
		Name   [1]uint16
	}
	offset := unsafe.Offsetof(renameInfo{}.Name)
	size := int(offset) + len(name)*2
	buffer := make([]uint64, (size+7)/8)
	info := (*renameInfo)(unsafe.Pointer(&buffer[0]))
	info.Flags = 0x1 | 0x2 // FILE_RENAME_REPLACE_IF_EXISTS | FILE_RENAME_POSIX_SEMANTICS
	info.Length = uint32((len(name) - 1) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Add(unsafe.Pointer(info), offset)), len(name)), name)
	err = windows.SetFileInformationByHandle(handle, windows.FileRenameInfoEx, (*byte)(unsafe.Pointer(info)), uint32(size))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) {
		return os.Rename(source, destination)
	}
	return err
}
