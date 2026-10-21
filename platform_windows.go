//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procLogicalDrives = kernel32.NewProc("GetLogicalDrives")
	procDriveType     = kernel32.NewProc("GetDriveTypeW")
	procVolumeInfo    = kernel32.NewProc("GetVolumeInformationW")
	procFreeSpace     = kernel32.NewProc("GetDiskFreeSpaceExW")
)

func openRaw(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// listDrives lists every drive letter on a removable or fixed disk: SD
// cards, USB sticks, external disks and the laptop's own drives. Empty card
// reader slots are left out; a card Windows cannot read (RAW) is kept, since
// it may still hold recoverable files.
func listDrives() []Drive {
	mask, _, _ := procLogicalDrives.Call()
	system := strings.ToUpper(os.Getenv("SystemDrive"))
	var out []Drive
	for i := 0; i < 26; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		letter := string(rune('A'+i)) + ":"
		root, _ := syscall.UTF16PtrFromString(letter + `\`)
		t, _, _ := procDriveType.Call(uintptr(unsafe.Pointer(root)))
		if t != 2 && t != 3 { // removable, fixed
			continue
		}
		var label, fs [261]uint16
		ok, _, e := procVolumeInfo.Call(uintptr(unsafe.Pointer(root)), uintptr(unsafe.Pointer(&label[0])), 261,
			0, 0, 0, uintptr(unsafe.Pointer(&fs[0])), 261)
		fsName := syscall.UTF16ToString(fs[:])
		if ok == 0 {
			if e == syscall.Errno(21) { // ERROR_NOT_READY
				continue // card reader slot with no card in it
			}
			fsName = "RAW"
		}
		var total uint64
		procFreeSpace.Call(uintptr(unsafe.Pointer(root)), 0, uintptr(unsafe.Pointer(&total)), 0)
		size := int64(total)
		if size == 0 {
			if f, err := openRaw(`\\.\` + letter); err == nil {
				size = deviceSize(f)
				f.Close()
			}
		}
		out = append(out, Drive{
			Path: `\\.\` + letter, Letter: letter,
			Label: syscall.UTF16ToString(label[:]), FS: fsName,
			Size: size, Removable: t == 2, System: letter == system,
		})
	}
	return out
}

// deviceSize returns the size in bytes of a volume or file.
func deviceSize(f *os.File) int64 {
	const ioctlDiskGetLengthInfo = 0x7405C
	var n int64
	var ret uint32
	if err := syscall.DeviceIoControl(syscall.Handle(f.Fd()), ioctlDiskGetLengthInfo, nil, 0,
		(*byte)(unsafe.Pointer(&n)), 8, &ret, nil); err == nil && n > 0 {
		return n
	}
	if fi, err := f.Stat(); err == nil {
		return fi.Size()
	}
	return 0
}

func openFolder(dir string) { exec.Command("explorer", dir).Start() }

// sameDrive reports whether dir is on the drive being recovered.
func sameDrive(source, dir string) bool {
	src := strings.ToUpper(strings.TrimPrefix(source, `\\.\`))
	return len(dir) >= 2 && strings.ToUpper(dir[:2]) == src
}

func prepareSource(string) {}

func defaultOut() string { return filepath.Join(os.Getenv("USERPROFILE"), "Desktop") }

const permissionHelp = "Windows did not allow reading this drive. Close DataRecovery and open it again, choosing Yes when Windows asks for permission."
