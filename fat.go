package main

import (
	"errors"
	"strings"
	"unicode/utf16"
)

func (v *Volume) initFAT(b []byte) error {
	bps := int64(le16(b[11:]))
	spc := int64(b[13])
	rsvd := int64(le16(b[14:]))
	nfats := int64(b[16])
	rootEnt := int64(le16(b[17:]))
	total := int64(le16(b[19:]))
	if total == 0 {
		total = int64(le32(b[32:]))
	}
	fatSz := int64(le16(b[22:]))
	if fatSz == 0 {
		fatSz = int64(le32(b[36:]))
	}
	rootSecs := (rootEnt*32 + bps - 1) / bps
	firstData := rsvd + nfats*fatSz + rootSecs
	if total <= firstData || fatSz == 0 {
		return errors.New("damaged FAT boot sector")
	}
	clusters := (total - firstData) / spc
	v.ClusterSize = bps * spc
	v.dataStart = firstData * bps
	v.clusterCount = uint32(clusters)
	switch {
	case clusters < 4085:
		v.Kind, v.fatBits, v.eoc = "FAT12", 12, 0xFF8
	case clusters < 65525:
		v.Kind, v.fatBits, v.eoc = "FAT16", 16, 0xFFF8
	default:
		v.Kind, v.fatBits, v.eoc = "FAT32", 32, 0x0FFFFFF8
	}
	v.fat = make([]byte, fatSz*bps)
	n, _ := v.dev.ReadAt(v.fat, rsvd*bps)
	v.fat = v.fat[:n]

	if v.Kind == "FAT32" {
		root, _ := v.chain(le32(b[44:]), 1<<16)
		v.rootData = v.readRuns(toRuns(root), 0)
		v.Label = strings.TrimSpace(string(b[71:82]))
	} else {
		v.rootData = make([]byte, rootSecs*bps)
		n, _ := v.dev.ReadAt(v.rootData, (rsvd+nfats*fatSz)*bps)
		v.rootData = v.rootData[:n]
		v.Label = strings.TrimSpace(string(b[43:54]))
	}
	for i := 0; i+32 <= len(v.rootData) && v.rootData[i] != 0; i += 32 {
		e := v.rootData[i:]
		if e[11]&0x08 != 0 && e[11] != 0x0F && e[0] != 0xE5 {
			v.Label = strings.TrimSpace(string(e[:11]))
		}
	}
	if v.Label == "NO NAME" {
		v.Label = ""
	}
	return nil
}

func sfnChecksum(n []byte) byte {
	var s byte
	for _, c := range n[:11] {
		s = (s&1)<<7 + s>>1 + c
	}
	return s
}

func shortName(e []byte) string {
	base := strings.TrimRight(string(e[0:8]), " ")
	ext := strings.TrimRight(string(e[8:11]), " ")
	if e[12]&0x08 != 0 {
		base = strings.ToLower(base)
	}
	if e[12]&0x10 != 0 {
		ext = strings.ToLower(ext)
	}
	if ext != "" {
		return base + "." + ext
	}
	return base
}

// longName rebuilds a long file name from the LFN entries that precede a
// short entry. Deleting a file overwrites the first byte of the short name
// (and of each LFN entry), so for deleted files the first character is
// recovered by finding the one that makes the stored checksum match.
func longName(parts [][]byte, sfn []byte, deleted bool) (string, bool) {
	if len(parts) == 0 {
		return "", false
	}
	sum := parts[0][13]
	for _, p := range parts {
		if p[13] != sum {
			return "", false
		}
	}
	if deleted {
		fixed := false
		n := append([]byte(nil), sfn[:11]...)
		for c := byte(0x21); c < 0x7F; c++ {
			n[0] = c
			if sfnChecksum(n) == sum {
				sfn[0], fixed = c, true
				break
			}
		}
		if !fixed {
			return "", false
		}
	} else if sfnChecksum(sfn) != sum {
		return "", false
	}
	var u []uint16
	for j := len(parts) - 1; j >= 0; j-- {
		p := parts[j]
		for _, r := range [][2]int{{1, 11}, {14, 26}, {28, 32}} {
			for k := r[0]; k < r[1]; k += 2 {
				u = append(u, le16(p[k:]))
			}
		}
	}
	for k, c := range u {
		if c == 0 || c == 0xFFFF {
			u = u[:k]
			break
		}
	}
	return string(utf16.Decode(u)), len(u) > 0
}

func (v *Volume) walkFAT(data []byte, dir string, parentDeleted bool, depth int) {
	if v.Stop.Load() {
		return
	}
	var lfn [][]byte
	for i := 0; i+32 <= len(data); i += 32 {
		e := append([]byte(nil), data[i:i+32]...)
		if e[0] == 0 {
			break
		}
		attr := e[11]
		if attr == 0x0F {
			lfn = append(lfn, e)
			continue
		}
		parts := lfn
		lfn = nil
		if attr&0x08 != 0 || attr&0xC0 != 0 || e[0] == '.' {
			continue
		}
		deleted := e[0] == 0xE5
		name, ok := longName(parts, e, deleted)
		if !ok {
			if deleted {
				e[0] = '_'
			}
			name = shortName(e)
		}
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "/\x00") {
			continue
		}
		deleted = deleted || parentDeleted
		first := uint32(le16(e[26:]))
		if v.Kind == "FAT32" {
			first |= uint32(le16(e[20:])) << 16
		}
		if !v.valid(first) {
			continue
		}
		size := int64(le32(e[28:]))
		p := dir + "/" + name

		if attr&0x10 != 0 {
			if v.visited[first] || depth > 64 {
				continue
			}
			v.visited[first] = true
			var sub []byte
			if deleted {
				sub = v.readDeletedFATDir(first, p)
			} else {
				cs, _ := v.chain(first, 1<<16)
				v.markKnown(toRuns(cs))
				sub = v.readRuns(toRuns(cs), 0)
			}
			v.walkFAT(sub, p, deleted, depth+1)
			continue
		}
		if deleted {
			// The FAT chain of a deleted file is wiped, so assume it was stored in one piece.
			v.addEntry(p, size, dosTime(le16(e[24:]), le16(e[22:])), v.contiguous(first, size))
		}
	}
}

// readDeletedFATDir reads a deleted directory whose cluster chain is gone,
// assuming it is contiguous and stopping where the data stops looking like
// directory entries. A directory that stops before its end marker is noted
// in v.unfinished so the deep scan can attach its lost pieces.
func (v *Volume) readDeletedFATDir(first uint32, p string) []byte {
	var out []byte
	for c := first; v.valid(c) && c < first+256; c++ {
		b := v.readCluster(c)
		if c == first && (len(b) < 64 || b[0] != '.' || b[1] != ' ') {
			return nil
		}
		ended, ok := scanDirCluster(b)
		if !ok || (c != first && (v.allocated(c) || !v.looksLikeDir(b))) {
			v.unfinished = append(v.unfinished, &unfinishedDir{p, first, lfnTail(out)})
			break
		}
		v.markKnown([]Run{{c, 1}})
		out = append(out, b...)
		if ended {
			break
		}
	}
	return out
}

// unfinishedDir is a deleted FAT folder whose entries stop before the end
// marker; tail holds long-name entries cut off at the end of its last piece.
type unfinishedDir struct {
	path  string
	first uint32
	tail  []byte
}

func lfnTail(b []byte) []byte {
	i := len(b) / 32 * 32
	for i >= 32 && b[i-32+11] == 0x0F && b[i-32] != 0 {
		i -= 32
	}
	return append([]byte(nil), b[i:len(b)/32*32]...)
}

func scanDirCluster(b []byte) (ended, ok bool) {
	for i := 0; i+32 <= len(b); i += 32 {
		e := b[i:]
		if e[0] == 0 {
			return true, true
		}
		if e[11]&0xC0 != 0 || (e[11] != 0x0F && e[0] < 0x20 && e[0] != 0x05) {
			return false, false
		}
	}
	return false, true
}
