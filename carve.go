package main

import (
	"bytes"
	"fmt"
	"time"
)

// Carve is the deep scan. It reads the start of every free cluster that no
// found entry covers, looking for two things: pieces of deleted folders whose
// link from their parent is gone (these still hold file names), and the
// start of photos and videos. Folder pieces are used first so that as many
// files as possible keep their names; the rest are recovered by content
// under /_Found without names.
func (v *Volume) Carve() {
	type hit struct {
		c           uint32
		ext, folder string
	}
	var dirs []uint32
	var hits []hit

	total := int64(v.clusterCount)
	cs := v.ClusterSize
	head := min(cs, 4096)
	batch := uint32(max(1, (4<<20)/cs))
	skip := func(c uint32) bool { return v.allocated(c) || v.isKnown(c) }

	last := v.clusterCount + 2
	for c := uint32(2); c < last; {
		if v.Stop.Load() {
			return
		}
		if skip(c) {
			c++
			continue
		}
		n := uint32(1)
		for c+n < last && n < batch && !skip(c+n) {
			n++
		}
		v.Progress("Deep scan: searching free space", int64(c-2), total)

		var buf []byte
		if cs < 64<<10 {
			buf = make([]byte, int64(n)*cs)
			got, _ := v.dev.ReadAt(buf, v.clusterOffset(c))
			buf = buf[:got]
		}
		for k := uint32(0); k < n; k++ {
			var h []byte
			if buf != nil {
				o := int64(k) * cs
				if o+head > int64(len(buf)) {
					break
				}
				h = buf[o : o+head]
			} else {
				h = make([]byte, head)
				if got, _ := v.dev.ReadAt(h, v.clusterOffset(c+k)); int64(got) < head {
					continue
				}
			}
			if v.looksLikeDir(h) {
				dirs = append(dirs, c+k)
			} else if ext, folder := sniff(h); ext != "" {
				hits = append(hits, hit{c + k, ext, folder})
			}
		}
		c += n
	}

	for _, c := range dirs {
		if !v.isKnown(c) {
			v.walkOrphanDir(c)
		}
	}

	for i, h := range hits {
		if v.Stop.Load() {
			return
		}
		v.Progress("Deep scan: recovering photos and videos without names", int64(i), int64(len(hits)))
		if v.isKnown(h.c) {
			continue
		}
		size := v.measure(h.ext, v.clusterOffset(h.c))
		if size <= 0 {
			continue
		}
		v.addEntry(fmt.Sprintf("/_Found/%s/f%09d.%s", h.folder, h.c, h.ext), size, time.Time{}, v.contiguous(h.c, size))
		v.Entries[len(v.Entries)-1].Carved = true
	}
}

// looksLikeDir reports whether a cluster's first bytes are directory entries
// that describe at least one file.
func (v *Volume) looksLikeDir(h []byte) bool {
	files := 0
	if v.Kind == "exFAT" {
		for i := 0; i+64 <= len(h); i += 32 {
			switch t := h[i]; t {
			case 0x00:
				return files > 0
			case 0x85, 0x05:
				sc := int(h[i+1])
				if sc < 2 || sc > 18 || h[i+32]&0x7F != 0x40 {
					return false
				}
				files++
			case 0xC0, 0x40, 0xC1, 0x41, 0x81, 0x82, 0x83, 0xA0, 0x20, 0xA1, 0x21, 0xA2, 0x22:
			default:
				return false
			}
		}
		return files > 0
	}
	for i := 0; i+32 <= len(h); i += 32 {
		e := h[i:]
		if e[0] == 0 {
			break
		}
		if e[11] == 0x0F {
			if le16(e[26:]) != 0 || e[12] != 0 {
				return false
			}
			continue
		}
		if e[11]&0xC0 != 0 || (e[0] < 0x20 && e[0] != 0x05) {
			return false
		}
		for _, ch := range e[1:11] {
			if ch < 0x20 || bytes.IndexByte([]byte(`"*+,/:;<=>?[\]|`), ch) >= 0 {
				return false
			}
		}
		files++
	}
	return files > 0
}

// walkOrphanDir reads a folder piece found by the deep scan. A FAT folder's
// first piece starts with a "." entry. A later piece is taken to continue
// the closest unfinished deleted folder that starts before it, since FAT
// gives a growing folder the next free cluster; long names cut at the
// piece boundary are joined back and still have to pass their checksum.
func (v *Volume) walkOrphanDir(c uint32) {
	p := fmt.Sprintf("/_LostFolders/folder_%d", c)
	v.markKnown([]Run{{c, 1}})
	if v.Kind == "exFAT" {
		v.walkExFAT(v.readCluster(c), p, true, 1)
		return
	}
	b := v.readCluster(c)
	if len(b) >= 2 && b[0] == '.' && b[1] == ' ' {
		v.visited[c] = true
		v.walkFAT(v.readDeletedFATDir(c, p), p, true, 1)
		return
	}
	var owner *unfinishedDir
	for _, u := range v.unfinished {
		if u.first < c && (owner == nil || u.first > owner.first) {
			owner = u
		}
	}
	if owner == nil {
		v.walkFAT(b, p, true, 1)
		return
	}
	v.walkFAT(append(owner.tail, b...), owner.path, true, 1)
	owner.tail = lfnTail(b)
}

var pngSig = []byte("\x89PNG\r\n\x1a\n")

func sniff(h []byte) (ext, folder string) {
	switch {
	case len(h) >= 4 && h[0] == 0xFF && h[1] == 0xD8 && h[2] == 0xFF:
		return "jpg", "Photos"
	case bytes.HasPrefix(h, pngSig):
		return "png", "Photos"
	case len(h) >= 12 && string(h[4:8]) == "ftyp":
		switch b := string(h[8:12]); {
		case b == "qt  ":
			return "mov", "Videos"
		case b == "heic" || b == "heix" || b == "mif1" || b == "msf1" || b == "hevc":
			return "heic", "Photos"
		case b[:3] == "3gp" || b[:3] == "3g2":
			return "3gp", "Videos"
		case b == "M4A " || b == "M4B ":
			return "", ""
		}
		return "mp4", "Videos"
	}
	return "", ""
}

// stream reads a byte range of the volume through a 1 MiB window.
type stream struct {
	v      *Volume
	start  int64
	limit  int64
	blk    []byte
	blkPos int64
}

func (s *stream) at(p int64) (byte, bool) {
	if p < 0 || p >= s.limit {
		return 0, false
	}
	if s.blk == nil || p < s.blkPos || p >= s.blkPos+int64(len(s.blk)) {
		s.blkPos = p &^ (1<<20 - 1)
		b := make([]byte, 1<<20)
		n, _ := s.v.dev.ReadAt(b, s.start+s.blkPos)
		s.blk = b[:n]
		if p >= s.blkPos+int64(n) {
			return 0, false
		}
	}
	return s.blk[p-s.blkPos], true
}

func (s *stream) be(p int64, n int) (uint64, bool) {
	var x uint64
	for i := int64(0); i < int64(n); i++ {
		b, ok := s.at(p + i)
		if !ok {
			return 0, false
		}
		x = x<<8 | uint64(b)
	}
	return x, true
}

func (v *Volume) measure(ext string, off int64) int64 {
	limit := int64(128 << 20)
	if ext == "mp4" || ext == "mov" || ext == "3gp" || ext == "heic" {
		limit = 16 << 30
	}
	s := &stream{v: v, start: off, limit: min(limit, v.end()-off)}
	switch ext {
	case "jpg":
		return measureJPEG(s)
	case "png":
		return measurePNG(s)
	}
	return measureISO(s, ext == "heic")
}

func measureJPEG(s *stream) int64 {
	p := int64(2)
	for seg := 0; ; seg++ {
		b, ok := s.at(p)
		if !ok || b != 0xFF {
			return 0
		}
		for b == 0xFF {
			p++
			if b, ok = s.at(p); !ok {
				return 0
			}
		}
		m := b
		p++
		if seg == 0 && !(m >= 0xC0 && m <= 0xFE) {
			return 0
		}
		if m == 0xD9 {
			if p < 2048 {
				return 0
			}
			return p
		}
		if m == 0x01 || (m >= 0xD0 && m <= 0xD7) {
			continue
		}
		l, ok := s.be(p, 2)
		if !ok || l < 2 {
			return 0
		}
		p += int64(l)
		if m != 0xDA {
			continue
		}
		// Entropy-coded data runs until a marker other than a stuffed 0xFF00 or a restart.
		for {
			b, ok := s.at(p)
			if !ok {
				return 0
			}
			if b != 0xFF {
				p++
				continue
			}
			n, ok := s.at(p + 1)
			if !ok {
				return 0
			}
			if n == 0xFF {
				p++
				continue
			}
			if n == 0x00 || (n >= 0xD0 && n <= 0xD7) {
				p += 2
				continue
			}
			break
		}
	}
}

func measurePNG(s *stream) int64 {
	p := int64(8)
	for {
		l, ok := s.be(p, 4)
		if !ok || l > 1<<30 {
			return 0
		}
		t, ok := s.be(p+4, 4)
		if !ok || !isFourCC(uint32(t)) {
			return 0
		}
		p += 12 + int64(l)
		if t == 0x49454E44 { // IEND
			if p > s.limit {
				return 0
			}
			return p
		}
	}
}

var isoBoxes = map[string]bool{"ftyp": true, "moov": true, "mdat": true, "free": true, "skip": true,
	"wide": true, "uuid": true, "meta": true, "pdin": true, "moof": true, "mfra": true, "styp": true,
	"sidx": true, "udta": true, "pnot": true, "junk": true, "prfl": true, "beam": true}

func measureISO(s *stream, image bool) int64 {
	p := int64(0)
	var moov, mdat, meta bool
	for i := 0; i < 1024; i++ {
		size, ok := s.be(p, 4)
		if !ok {
			break
		}
		t, ok := s.be(p+4, 4)
		if !ok || !isFourCC(uint32(t)) {
			break
		}
		name := string([]byte{byte(t >> 24), byte(t >> 16), byte(t >> 8), byte(t)})
		if !isoBoxes[name] {
			break
		}
		if size == 1 {
			if size, ok = s.be(p+8, 8); !ok {
				break
			}
		}
		if size < 8 {
			break
		}
		switch name {
		case "moov":
			moov = true
		case "mdat":
			mdat = true
		case "meta":
			meta = true
		}
		if p+int64(size) > s.limit {
			return 0
		}
		p += int64(size)
	}
	if mdat && (moov || (image && meta)) {
		return p
	}
	return 0
}

func isFourCC(t uint32) bool {
	for i := 0; i < 4; i++ {
		c := byte(t >> (8 * i))
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ' ' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
