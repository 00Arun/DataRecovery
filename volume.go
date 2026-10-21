package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"path"
	"strings"
	"sync/atomic"
	"time"
)

type Run struct{ Start, Count uint32 }

// Entry is one recoverable (deleted or carved) file.
type Entry struct {
	ID      int       `json:"id"`
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
	Status  string    `json:"status"` // good, partial, overwritten
	Kind    string    `json:"kind"`   // photo, video, other
	Carved  bool      `json:"carved"`
	runs    []Run     // clusters; a run with Start 0 is a sparse hole
	inline  []byte    // small NTFS files stored inside their MFT record
}

type Volume struct {
	dev          *Device
	Kind         string
	Label        string
	ClusterSize  int64
	dataStart    int64 // byte offset of cluster 2
	clusterCount uint32
	fat          []byte
	fatBits      int
	eoc          uint32 // smallest end-of-chain marker
	bitmap       []byte // exFAT allocation bitmap
	rootData     []byte
	known        []uint64 // clusters already claimed by a found entry
	visited      map[uint32]bool
	unfinished   []*unfinishedDir
	mftRecSize   int64 // NTFS: bytes per file record
	mftRuns      []Run // NTFS: where the file table is
	mftSize      int64
	Entries      []*Entry

	Stop     atomic.Bool
	Progress func(msg string, done, total int64)
}

func le16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
func le64(b []byte) uint64 { return binary.LittleEndian.Uint64(b) }

func OpenVolume(dev *Device) (*Volume, error) {
	b := make([]byte, 512)
	if _, err := dev.ReadAt(b, 0); err != nil {
		return nil, err
	}
	v := &Volume{dev: dev, visited: map[uint32]bool{}, Progress: func(string, int64, int64) {}}
	var err error
	switch bootKind(b) {
	case "exFAT":
		err = v.initExFAT(b)
	case "FAT":
		err = v.initFAT(b)
	case "NTFS":
		err = v.initNTFS(b)
	default:
		err = v.initRaw()
	}
	if err != nil {
		return nil, err
	}
	v.known = make([]uint64, (int(v.clusterCount)+2)/64+1)
	return v, nil
}

func (v *Volume) Scan() {
	v.Progress("Reading folders", 0, 0)
	switch v.Kind {
	case "exFAT":
		v.walkExFAT(v.rootData, "", false, 0)
	case "NTFS":
		v.walkNTFS()
	case "Raw":
		// Nothing to read: names are only known to supported file systems.
	default:
		v.walkFAT(v.rootData, "", false, 0)
	}
}

func (v *Volume) valid(c uint32) bool { return c >= 2 && c < v.clusterCount+2 }

func (v *Volume) clusterOffset(c uint32) int64 {
	return v.dataStart + int64(c-2)*v.ClusterSize
}

func (v *Volume) end() int64 { return v.clusterOffset(v.clusterCount + 2) }

func (v *Volume) fatNext(c uint32) uint32 {
	switch v.fatBits {
	case 32:
		i := int(c) * 4
		if i+4 > len(v.fat) {
			return 0
		}
		x := le32(v.fat[i:])
		if v.Kind == "FAT32" {
			x &= 0x0FFFFFFF
		}
		return x
	case 16:
		i := int(c) * 2
		if i+2 > len(v.fat) {
			return 0
		}
		return uint32(le16(v.fat[i:]))
	default:
		i := int(c) + int(c)/2
		if i+2 > len(v.fat) {
			return 0
		}
		x := uint32(le16(v.fat[i:]))
		if c&1 == 1 {
			return x >> 4
		}
		return x & 0xFFF
	}
}

func (v *Volume) allocated(c uint32) bool {
	if v.bitmap != nil {
		i := c - 2
		return int(i/8) < len(v.bitmap) && v.bitmap[i/8]&(1<<(i%8)) != 0
	}
	return v.fatNext(c) != 0
}

func (v *Volume) isKnown(c uint32) bool { return v.known[c/64]&(1<<(c%64)) != 0 }

func (v *Volume) markKnown(runs []Run) {
	for _, r := range runs {
		for c := r.Start; c < r.Start+r.Count && v.valid(c); c++ {
			v.known[c/64] |= 1 << (c % 64)
		}
	}
}

// chain follows the FAT from first; ok reports a proper end-of-chain.
func (v *Volume) chain(first uint32, max int) (cs []uint32, ok bool) {
	seen := map[uint32]bool{}
	c := first
	for v.valid(c) && !seen[c] && len(cs) < max {
		seen[c] = true
		cs = append(cs, c)
		c = v.fatNext(c)
	}
	return cs, c >= v.eoc
}

func toRuns(cs []uint32) []Run {
	var runs []Run
	for _, c := range cs {
		if n := len(runs); n > 0 && runs[n-1].Start+runs[n-1].Count == c {
			runs[n-1].Count++
		} else {
			runs = append(runs, Run{c, 1})
		}
	}
	return runs
}

func (v *Volume) contiguous(first uint32, size int64) []Run {
	n := uint32((size + v.ClusterSize - 1) / v.ClusterSize)
	if n == 0 {
		n = 1
	}
	if last := v.clusterCount + 2; first+n > last {
		n = last - first
	}
	return []Run{{first, n}}
}

func (v *Volume) readCluster(c uint32) []byte {
	b := make([]byte, v.ClusterSize)
	n, _ := v.dev.ReadAt(b, v.clusterOffset(c))
	return b[:n]
}

func (v *Volume) readRuns(runs []Run, limit int64) []byte {
	var out []byte
	for _, r := range runs {
		b := make([]byte, int64(r.Count)*v.ClusterSize)
		n, _ := v.dev.ReadAt(b, v.clusterOffset(r.Start))
		out = append(out, b[:n]...)
		if limit > 0 && int64(len(out)) >= limit {
			return out[:limit]
		}
	}
	return out
}

// status reports how much of a deleted file's space is now used by other files.
func (v *Volume) status(runs []Run) string {
	var used, total int
	for _, r := range runs {
		for c := r.Start; c < r.Start+r.Count && r.Start != 0; c++ {
			total++
			if v.allocated(c) {
				used++
			}
		}
	}
	switch {
	case used == 0:
		return "good"
	case used == total:
		return "overwritten"
	}
	return "partial"
}

// systemJunk matches files that operating systems create on their own;
// nobody wants these back.
func systemJunk(p string) bool {
	base := path.Base(p)
	switch strings.ToLower(base) {
	case ".ds_store", "thumbs.db", "desktop.ini", "fseventsd-uuid", ".fseventsd", "indexervolumeguid":
		return true
	}
	return strings.HasPrefix(base, "._") || strings.Contains(p, "/.fseventsd/") ||
		strings.Contains(p, "/.Spotlight-V100/") || strings.Contains(p, "/.Trashes/") ||
		strings.Contains(p, "/System Volume Information/")
}

func (v *Volume) addEntry(p string, size int64, mtime time.Time, runs []Run) {
	if size <= 0 || size > v.end() || len(runs) == 0 || systemJunk(p) {
		return
	}
	e := &Entry{
		ID: len(v.Entries), Path: p, Size: size, ModTime: mtime,
		Status: v.status(runs), Kind: kindOf(p), runs: runs,
	}
	// SSDs erase the space of deleted files (TRIM), so the clusters can be
	// free yet hold only zeros. Check photos and videos, which people most
	// want back, so they are not shown as intact when nothing is left.
	if e.Status != "overwritten" && e.Kind != "other" && allZero(v.Head(e, 4096)) {
		e.Status = "overwritten"
	}
	v.Entries = append(v.Entries, e)
	v.markKnown(runs)
}

// addInline records a small file whose bytes are stored in file system metadata.
func (v *Volume) addInline(p string, data []byte, mtime time.Time) {
	if len(data) == 0 || systemJunk(p) {
		return
	}
	v.Entries = append(v.Entries, &Entry{
		ID: len(v.Entries), Path: p, Size: int64(len(data)), ModTime: mtime,
		Status: "good", Kind: kindOf(p), inline: data,
	})
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// initRaw treats a drive with no recognised file system as a plain run of
// 4 KiB blocks, so the deep scan can still search it.
func (v *Volume) initRaw() error {
	if v.dev.size <= 0 {
		return errors.New("could not read the size of this drive")
	}
	v.Kind = "Raw"
	v.ClusterSize = 4096
	v.clusterCount = uint32(v.dev.size / v.ClusterSize)
	return nil
}

// Extract writes the entry's bytes to w.
func (v *Volume) Extract(e *Entry, w io.Writer) error { return v.extract(e, w, e.Size) }

// Head returns up to n bytes from the start of the entry.
func (v *Volume) Head(e *Entry, n int64) []byte {
	var b bytes.Buffer
	v.extract(e, &b, min(n, e.Size))
	return b.Bytes()
}

func (v *Volume) extract(e *Entry, w io.Writer, left int64) error {
	if e.inline != nil {
		_, err := w.Write(e.inline[:min(left, int64(len(e.inline)))])
		return err
	}
	for _, r := range e.runs {
		off := v.clusterOffset(r.Start)
		n := int64(r.Count) * v.ClusterSize
		for n > 0 && left > 0 {
			chunk := min(n, left, 4<<20)
			b := make([]byte, chunk)
			if r.Start == 0 { // sparse hole: zeros
				if _, err := w.Write(b); err != nil {
					return err
				}
				n -= chunk
				left -= chunk
				continue
			}
			got, err := v.dev.ReadAt(b, off)
			if _, werr := w.Write(b[:got]); werr != nil {
				return werr
			}
			if err != nil && int64(got) < chunk {
				return err
			}
			off += chunk
			n -= chunk
			left -= chunk
		}
	}
	return nil
}

var photoExt = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".bmp": true,
	".heic": true, ".heif": true, ".webp": true, ".dng": true, ".raw": true, ".cr2": true, ".cr3": true,
	".nef": true, ".arw": true, ".orf": true, ".rw2": true, ".tif": true, ".tiff": true}
var videoExt = map[string]bool{".mp4": true, ".mov": true, ".3gp": true, ".mkv": true, ".avi": true,
	".m4v": true, ".wmv": true, ".flv": true, ".webm": true, ".mts": true, ".m2ts": true, ".ts": true}

func kindOf(p string) string {
	ext := strings.ToLower(path.Ext(p))
	switch {
	case photoExt[ext]:
		return "photo"
	case videoExt[ext]:
		return "video"
	}
	return "other"
}

// exifThumb returns the small preview JPEG that cameras and phones embed in
// a photo's EXIF block, or nil.
func exifThumb(b []byte) []byte {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return nil
	}
	for p := 2; p+4 <= len(b) && b[p] == 0xFF; {
		m := b[p+1]
		l := int(b[p+2])<<8 | int(b[p+3])
		if m == 0xDA || l < 2 {
			break
		}
		seg := b[p+4 : min(p+2+l, len(b))]
		if m == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
			if s := bytes.Index(seg[6:], []byte{0xFF, 0xD8, 0xFF}); s >= 0 {
				t := seg[6+s:]
				if e := bytes.LastIndex(t, []byte{0xFF, 0xD9}); e > 0 {
					return t[:e+2]
				}
			}
		}
		p += 2 + l
	}
	return nil
}

func dosTime(date, tm uint16) time.Time {
	if date == 0 {
		return time.Time{}
	}
	return time.Date(int(date>>9)+1980, time.Month(date>>5&15), int(date&31),
		int(tm>>11), int(tm>>5&63), int(tm&31)*2, 0, time.Local)
}
