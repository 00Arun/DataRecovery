//go:build !windows && !darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

func openRaw(path string) (*os.File, error) { return os.Open(path) }

func listDrives() []Drive { return nil }

func prepareSource(string) {}

func openFolder(dir string) { exec.Command("xdg-open", dir).Start() }

func sameDrive(source, dir string) bool { return false }

func defaultOut() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Desktop")
}

const permissionHelp = "Permission denied. Run DataRecovery as root (for example with sudo)."

func deviceSize(f *os.File) int64 {
	n, _ := f.Seek(0, 2)
	return n
}
