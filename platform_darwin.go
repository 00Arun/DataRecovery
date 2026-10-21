//go:build darwin

package main

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// openRaw opens a file or device for reading. Reading a card directly is
// reserved for administrators, so for devices macOS's authopen asks for an
// administrator password and hands back a read-only descriptor; the app
// itself keeps running as the normal user.
func openRaw(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err == nil || !errors.Is(err, os.ErrPermission) || !strings.HasPrefix(path, "/dev/") {
		return f, err
	}
	return authOpen(path)
}

func authOpen(path string) (*os.File, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}
	child := os.NewFile(uintptr(fds[1]), "authopen")
	cmd := exec.Command("/usr/libexec/authopen", "-stdoutpipe", "-o", strconv.Itoa(os.O_RDONLY), path)
	cmd.Stdout = child
	if err := cmd.Start(); err != nil {
		child.Close()
		syscall.Close(fds[0])
		return nil, err
	}
	child.Close()
	defer syscall.Close(fds[0])
	buf := make([]byte, 16)
	oob := make([]byte, syscall.CmsgSpace(4))
	_, oobn, _, _, rerr := syscall.Recvmsg(fds[0], buf, oob, 0)
	cmd.Wait()
	if rerr == nil && oobn > 0 {
		if msgs, err := syscall.ParseSocketControlMessage(oob[:oobn]); err == nil && len(msgs) > 0 {
			if fdl, err := syscall.ParseUnixRights(&msgs[0]); err == nil && len(fdl) > 0 {
				return os.NewFile(uintptr(fdl[0]), path), nil
			}
		}
	}
	return nil, os.ErrPermission
}

var (
	sliceRe   = regexp.MustCompile(`\b(disk\d+(s\d+)+)\s*$`)
	headerRe  = regexp.MustCompile(`^/dev/(disk\d+) \(([^)]*)\)`)
	bytesRe   = regexp.MustCompile(`\((\d+) Bytes\)`)
	skipParts = map[string]bool{"EFI": true, "Apple_APFS": true, "Apple_APFS_ISC": true,
		"Apple_APFS_Recovery": true, "Apple_Boot": true, "Microsoft Reserved": true, "Apple_partition_map": true}
	driveCache struct {
		sync.Mutex
		key    string
		drives []Drive
	}
)

// listDrives lists every drive a person might recover from: cards (including
// the MacBook's built-in SD slot), USB and external disks, other internal
// partitions, and the Mac's own data volume. System helper volumes and disk
// images are left out. The list is rebuilt only when diskutil's layout
// changes, so the app can poll it to notice drives being plugged in.
func listDrives() []Drive {
	out, err := exec.Command("diskutil", "list").Output()
	if err != nil {
		return nil
	}
	mounts, _ := exec.Command("mount").Output()
	key := string(out) + string(mounts)
	driveCache.Lock()
	defer driveCache.Unlock()
	if key == driveCache.key {
		return driveCache.drives
	}

	var drives []Drive
	add := func(id string, synthesized bool) {
		info := diskInfo(id)
		mp := info["Mount Point"]
		if skipParts[info["Partition Type"]] || skipParts[info["Partition Name"]] {
			return
		}
		system := mp == "/System/Volumes/Data"
		if synthesized && !system && !strings.HasPrefix(mp, "/Volumes/") {
			return // Preboot, Recovery, VM and other helper volumes
		}
		label := info["Volume Name"]
		if system {
			label = "Macintosh HD"
		}
		var size int64
		if m := bytesRe.FindStringSubmatch(info["Disk Size"]); m != nil {
			size, _ = strconv.ParseInt(m[1], 10, 64)
		}
		proto := info["Protocol"]
		drives = append(drives, Drive{
			Path: "/dev/r" + id, Letter: id, Label: label, FS: fsName(info["File System Personality"]), Size: size,
			Removable: info["Removable Media"] == "Removable" || info["Device Location"] == "External" ||
				proto == "USB" || proto == "Secure Digital" || proto == "Thunderbolt",
			System: system,
		})
	}

	var disk, kind string
	var slices []string
	flush := func() {
		if disk == "" || strings.Contains(kind, "disk image") {
			return
		}
		if len(slices) == 0 && !strings.Contains(kind, "synthesized") {
			add(disk, false) // a card formatted without a partition table
		}
		for _, s := range slices {
			add(s, strings.Contains(kind, "synthesized"))
		}
	}
	for _, line := range strings.Split(string(out), "\n") {
		if m := headerRe.FindStringSubmatch(line); m != nil {
			flush()
			disk, kind, slices = m[1], m[2], nil
		} else if m := sliceRe.FindStringSubmatch(line); m != nil && strings.Count(m[1], "s") == 2 { // diskNsM, not snapshots like diskNsMsK
			slices = append(slices, m[1])
		}
	}
	flush()
	driveCache.key, driveCache.drives = key, drives
	return drives
}

func fsName(personality string) string {
	switch p := strings.ToLower(personality); {
	case p == "exfat":
		return "exFAT"
	case strings.HasPrefix(p, "ms-dos "):
		return strings.ToUpper(strings.TrimPrefix(p, "ms-dos "))
	case strings.Contains(p, "apfs"):
		return "APFS"
	case strings.Contains(p, "hfs"):
		return "HFS+"
	case strings.Contains(p, "ntfs"):
		return "NTFS"
	}
	return personality
}

// deviceSize returns the size in bytes of a device or file.
func deviceSize(f *os.File) int64 {
	fi, err := f.Stat()
	if err != nil {
		return 0
	}
	if fi.Mode()&os.ModeDevice == 0 {
		return fi.Size()
	}
	const getBlockSize, getBlockCount = 0x40046418, 0x40086419 // DKIOCGETBLOCKSIZE, DKIOCGETBLOCKCOUNT
	var bs uint32
	var n uint64
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), getBlockSize, uintptr(unsafe.Pointer(&bs))); e != 0 {
		return 0
	}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), getBlockCount, uintptr(unsafe.Pointer(&n))); e != 0 {
		return 0
	}
	return int64(bs) * int64(n)
}

func diskInfo(id string) map[string]string {
	out, _ := exec.Command("diskutil", "info", id).Output()
	m := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), ":"); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

func deviceID(source string) string {
	if !strings.HasPrefix(source, "/dev/") {
		return ""
	}
	return strings.TrimPrefix(strings.TrimPrefix(source, "/dev/"), "r")
}

// prepareSource unmounts a card before scanning so macOS stops writing its
// own bookkeeping files to it while deleted files are being read.
func prepareSource(source string) {
	if id := deviceID(source); id != "" {
		exec.Command("diskutil", "unmount", id).Run()
	}
}

func openFolder(dir string) { exec.Command("open", dir).Start() }

func sameDrive(source, dir string) bool {
	id := deviceID(source)
	if id == "" {
		return false
	}
	mp := diskInfo(id)["Mount Point"]
	return mp != "" && (dir == mp || strings.HasPrefix(dir, mp+"/"))
}

func defaultOut() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Desktop")
}

const permissionHelp = "The drive could not be opened. Enter your Mac password when asked. If you did and it still fails, open System Settings > Privacy & Security > Full Disk Access, turn on DataRecovery, then quit and reopen DataRecovery."
