package main

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// NTFS keeps one record per file in the Master File Table (MFT). Deleting a
// file only clears the record's in-use flag, so its name, parent folder,
// size and cluster runs usually stay readable until the record is reused.
//
// Cluster numbers are stored as LCN+2 in Runs so that the shared
// clusterOffset (which counts from cluster 2) works unchanged.

type ntfsRec struct {
	name    string
	nameNS  byte
	parent  uint32
	dir     bool
	inUse   bool
	size    int64
	runs    []Run
	inline  []byte
	hasData bool
	mtime   time.Time
}

type ntfsExtent struct {
	vcn  int64
	runs []Run
}

func (v *Volume) initNTFS(b []byte) error {
	bps := int64(le16(b[11:]))
	spc := int64(b[13])
	if spc > 0x80 {
		spc = 1 << (256 - spc)
	}
	if bps == 0 || spc == 0 {
		return errors.New("damaged NTFS boot sector")
	}
	cs := bps * spc
	v.Kind = "NTFS"
	v.ClusterSize = cs
	v.clusterCount = uint32(int64(le64(b[40:])) * bps / cs)
	if c := int8(b[64]); c < 0 {
		v.mftRecSize = 1 << -c
	} else {
		v.mftRecSize = int64(c) * cs
	}
	if v.mftRecSize < 512 || v.mftRecSize > 64<<10 {
		return errors.New("damaged NTFS boot sector")
	}

	// Record 0 is the MFT itself; its data runs say where the whole table is.
	rec := make([]byte, v.mftRecSize)
	if _, err := v.dev.ReadAt(rec, int64(le64(b[48:]))*cs); err != nil {
		return err
	}
	if !applyFixups(rec) {
		return errors.New("could not read the NTFS file table")
	}
	r0, ok := v.parseMFTRecord(rec)
	if !ok || len(r0.runs) == 0 {
		return errors.New("could not read the NTFS file table")
	}
	v.mftRuns, v.mftSize = r0.runs, r0.size

	if r, ok := v.readMFTRecord(6); ok { // $Bitmap: which clusters are in use
		e := &Entry{Size: r.size, runs: r.runs, inline: r.inline}
		v.bitmap = v.Head(e, r.size)
	}
	if raw := v.rawMFTRecord(3); raw != nil { // $Volume: volume name
		forAttrs(raw, func(t uint32, a []byte) {
			if t == 0x60 && a[8] == 0 {
				off, n := int(le16(a[20:])), int(le32(a[16:]))
				if off+n <= len(a) {
					v.Label = decodeUTF16(a[off : off+n])
				}
			}
		})
	}
	return nil
}

// mftOffset maps record n to its byte offset on the volume.
func (v *Volume) mftOffset(n int64) (int64, bool) {
	pos := n * v.mftRecSize
	for _, r := range v.mftRuns {
		l := int64(r.Count) * v.ClusterSize
		if pos < l {
			if r.Start == 0 {
				return 0, false
			}
			return v.clusterOffset(r.Start) + pos, true
		}
		pos -= l
	}
	return 0, false
}

func (v *Volume) rawMFTRecord(n int64) []byte {
	off, ok := v.mftOffset(n)
	if !ok {
		return nil
	}
	b := make([]byte, v.mftRecSize)
	if _, err := v.dev.ReadAt(b, off); err != nil || !applyFixups(b) {
		return nil
	}
	return b
}

func (v *Volume) readMFTRecord(n int64) (*ntfsRec, bool) {
	b := v.rawMFTRecord(n)
	if b == nil {
		return nil, false
	}
	return v.parseMFTRecord(b)
}

// applyFixups undoes NTFS's torn-write protection: the last two bytes of
// every 512-byte stride are stored in the update sequence array.
func applyFixups(b []byte) bool {
	if len(b) < 48 || string(b[:4]) != "FILE" {
		return false
	}
	usa, cnt := int(le16(b[4:])), int(le16(b[6:]))
	if usa+2*cnt > len(b) || cnt == 0 {
		return false
	}
	for i := 1; i < cnt; i++ {
		p := i*512 - 2
		if p+2 > len(b) {
			break
		}
		if b[p] != b[usa] || b[p+1] != b[usa+1] {
			return false
		}
		b[p], b[p+1] = b[usa+2*i], b[usa+2*i+1]
	}
	return true
}

func forAttrs(b []byte, fn func(typ uint32, a []byte)) {
	for p := int(le16(b[20:])); p+16 <= len(b); {
		t := le32(b[p:])
		l := int(le32(b[p+4:]))
		if t == 0xFFFFFFFF || l < 16 || p+l > len(b) {
			return
		}
		fn(t, b[p:p+l])
		p += l
	}
}

// parseMFTRecord reads the name, parent, size and data location of a base
// record whose fixups are already applied. Records that extend another
// record are handled in walkNTFS.
func (v *Volume) parseMFTRecord(b []byte) (*ntfsRec, bool) {
	if len(b) < 48 || string(b[:4]) != "FILE" {
		return nil, false
	}
	flags := le16(b[22:])
	r := &ntfsRec{inUse: flags&1 != 0, dir: flags&2 != 0, nameNS: 255}
	forAttrs(b, func(t uint32, a []byte) {
		nonRes := a[8] != 0
		named := a[9] != 0
		switch {
		case t == 0x30 && !nonRes:
			off := int(le16(a[20:]))
			if off+66 > len(a) {
				return
			}
			fn := a[off:]
			n, ns := int(fn[64]), fn[65]
			// Keep the first long name; a DOS 8.3 name (namespace 2) is only a fallback.
			if 66+2*n > len(fn) || (r.name != "" && (ns == 2 || r.nameNS != 2)) {
				return
			}
			r.parent = uint32(le64(fn) & 0xFFFFFFFFFFFF)
			r.name = decodeUTF16(fn[66 : 66+2*n])
			r.nameNS = ns
			r.mtime = fileTime(le64(fn[16:]))
		case t == 0x80 && !named && !nonRes:
			off, n := int(le16(a[20:])), int(le32(a[16:]))
			if off+n <= len(a) {
				r.inline = append([]byte(nil), a[off:off+n]...)
				r.size = int64(n)
				r.hasData = true
			}
		case t == 0x80 && !named && nonRes && len(a) >= 64:
			if le64(a[16:]) == 0 { // first extent carries the real size
				r.size = int64(le64(a[48:]))
			}
			r.runs = append(r.runs, decodeRuns(a[le16(a[32:]):])...)
			r.hasData = true
		}
	})
	return r, true
}

// decodeRuns turns an NTFS run list into cluster runs (LCN+2, or 0 for a hole).
func decodeRuns(b []byte) []Run {
	var runs []Run
	var lcn int64
	for p := 0; p < len(b) && b[p] != 0; {
		h := b[p]
		ls, os := int(h&0xF), int(h>>4)
		if ls == 0 || ls > 8 || os > 8 || p+1+ls+os > len(b) {
			break
		}
		var n int64
		for i := ls - 1; i >= 0; i-- {
			n = n<<8 | int64(b[p+1+i])
		}
		if os == 0 {
			runs = append(runs, Run{0, uint32(n)})
		} else {
			var d int64
			for i := os - 1; i >= 0; i-- {
				d = d<<8 | int64(b[p+1+ls+i])
			}
			if b[p+ls+os]&0x80 != 0 { // sign-extend the relative offset
				d -= 1 << (8 * os)
			}
			lcn += d
			runs = append(runs, Run{uint32(lcn + 2), uint32(n)})
		}
		p += 1 + ls + os
	}
	return runs
}

func fileTime(ft uint64) time.Time {
	if ft == 0 {
		return time.Time{}
	}
	return time.Unix(0, (int64(ft)-116444736000000000)*100)
}

// walkNTFS reads the whole MFT and lists every deleted file with the
// folder path rebuilt from the parent references.
func (v *Volume) walkNTFS() {
	recs := map[uint32]*ntfsRec{}
	ext := map[uint32][]ntfsExtent{}
	total := v.mftSize / v.mftRecSize
	chunk := max(1, (4<<20)/v.mftRecSize)

	for n := int64(0); n < total && !v.Stop.Load(); n += chunk {
		v.Progress("Reading the file table", n, total)
		for i := n; i < min(n+chunk, total); i++ {
			off, ok := v.mftOffset(i)
			if !ok {
				continue
			}
			b := make([]byte, v.mftRecSize)
			if _, err := v.dev.ReadAt(b, off); err != nil || !applyFixups(b) {
				continue
			}
			if base := uint32(le64(b[32:]) & 0xFFFFFFFFFFFF); base != 0 {
				// Extension record of a large, fragmented file: keep its runs.
				forAttrs(b, func(t uint32, a []byte) {
					if t == 0x80 && a[8] != 0 && a[9] == 0 && len(a) >= 64 {
						ext[base] = append(ext[base], ntfsExtent{int64(le64(a[16:])), decodeRuns(a[le16(a[32:]):])})
					}
				})
				continue
			}
			if r, ok := v.parseMFTRecord(b); ok && r.name != "" {
				recs[uint32(i)] = r
			}
		}
	}

	for id, xs := range ext {
		r := recs[id]
		if r == nil {
			continue
		}
		sort.Slice(xs, func(i, j int) bool { return xs[i].vcn < xs[j].vcn })
		for _, x := range xs {
			r.runs = append(r.runs, x.runs...)
		}
	}

	paths := map[uint32]string{5: ""} // record 5 is the root folder
	var pathOf func(id uint32, depth int) string
	pathOf = func(id uint32, depth int) string {
		if p, ok := paths[id]; ok {
			return p
		}
		r := recs[id]
		if r == nil || !r.dir || depth > 64 || r.parent == id {
			return fmt.Sprintf("/_LostFolders/folder_%d", id)
		}
		p := pathOf(r.parent, depth+1) + "/" + r.name
		paths[id] = p
		return p
	}

	ids := make([]uint32, 0, len(recs))
	for id := range recs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		r := recs[id]
		if r.inUse || r.dir || !r.hasData || id < 24 {
			continue
		}
		p := pathOf(r.parent, 0) + "/" + r.name
		if r.inline != nil {
			v.addInline(p, r.inline, r.mtime)
		} else {
			v.addEntry(p, r.size, r.mtime, r.runs)
		}
	}
}
