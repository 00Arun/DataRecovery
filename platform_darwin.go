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
	"syscall"
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

var sliceRe = regexp.MustCompile(`\b(disk\d+(s\d+)?)\s*$`)

func listDrives() []Drive {
	out, err := exec.Command("diskutil", "list", "external", "physical").Output()
	if err != nil {
		return nil
	}
	var ids []string
	disks := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "/dev/disk") {
			d := strings.Fields(line)[0][len("/dev/"):]
			disks[d] = false
			ids = append(ids, d)
			continue
		}
		if m := sliceRe.FindStringSubmatch(line); m != nil && m[2] != "" {
			disks[strings.TrimSuffix(m[1], m[2])] = true
			ids = append(ids, m[1])
		}
	}
	var drives []Drive
	for _, id := range ids {
		if hasSlices, isDisk := disks[id]; isDisk && hasSlices {
			continue // list the partitions, not the whole disk
		}
		info := diskInfo(id)
		fs := info["File System Personality"]
		switch strings.ToLower(fs) {
		case "exfat":
			fs = "exFAT"
		case "ms-dos fat32", "ms-dos fat16", "ms-dos fat12":
			fs = strings.TrimPrefix(fs, "MS-DOS ")
		}
		var size int64
		if m := regexp.MustCompile(`\((\d+) Bytes\)`).FindStringSubmatch(info["Disk Size"]); m != nil {
			size, _ = strconv.ParseInt(m[1], 10, 64)
		}
		if strings.Contains(strings.ToLower(info["Partition Type"]), "efi") {
			continue
		}
		drives = append(drives, Drive{
			Path: "/dev/r" + id, Letter: id, Label: info["Volume Name"], FS: fs, Size: size,
			Removable: true,
		})
	}
	return drives
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
