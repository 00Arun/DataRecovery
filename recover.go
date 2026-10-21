package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type RecoverResult struct {
	Root   string   `json:"root"`
	OK     int      `json:"ok"`
	Failed []string `json:"failed"`
}

// Recover copies the chosen entries into outDir/<Recovered_label>/<original path>,
// keeping folder structure and modification times. Existing files are never
// overwritten; a numbered name is used instead.
func (v *Volume) Recover(ids []int, outDir string) RecoverResult {
	name := "Recovered"
	if l := safeName(v.Label); l != "" && l != "_" {
		name += "_" + l
	}
	root := uniquePath(filepath.Join(outDir, name+"_"+time.Now().Format("2006-01-02_1504")))
	res := RecoverResult{Root: root}
	var bytes, total int64
	for _, id := range ids {
		if id >= 0 && id < len(v.Entries) {
			total += v.Entries[id].Size
		}
	}
	for _, id := range ids {
		if v.Stop.Load() {
			break
		}
		if id < 0 || id >= len(v.Entries) {
			continue
		}
		e := v.Entries[id]
		v.Progress("Recovering "+e.Path, bytes, total)
		if err := v.recoverOne(e, root); err != nil {
			res.Failed = append(res.Failed, e.Path+": "+err.Error())
		} else {
			res.OK++
		}
		bytes += e.Size
	}
	v.Progress("Done", total, total)
	return res
}

func (v *Volume) recoverOne(e *Entry, root string) error {
	parts := strings.Split(strings.Trim(e.Path, "/"), "/")
	for i, p := range parts {
		parts[i] = safeName(p)
	}
	dest := uniquePath(filepath.Join(append([]string{root}, parts...)...))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	err = v.Extract(e, w)
	if ferr := w.Flush(); err == nil {
		err = ferr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && !e.ModTime.IsZero() {
		os.Chtimes(dest, e.ModTime, e.ModTime)
	}
	return err
}

func uniquePath(p string) string {
	if _, err := os.Lstat(p); os.IsNotExist(err) {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 1; ; i++ {
		q := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Lstat(q); os.IsNotExist(err) {
			return q
		}
	}
}

var reservedNames = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true,
	"COM8": true, "COM9": true, "LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true}

// safeName makes one path component valid on Windows.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, s)
	s = strings.TrimRight(s, ". ")
	if s == "" {
		return "_"
	}
	if reservedNames[strings.ToUpper(strings.TrimSuffix(s, filepath.Ext(s)))] {
		s = "_" + s
	}
	return s
}
