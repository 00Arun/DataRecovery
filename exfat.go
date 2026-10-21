package main

import (
	"errors"
	"strings"
	"unicode/utf16"
)

func (v *Volume) initExFAT(b []byte) error {
	bps := int64(1) << b[108]
	spc := int64(1) << b[109]
	v.Kind = "exFAT"
	v.ClusterSize = bps * spc
	v.dataStart = int64(le32(b[88:])) * bps
	v.clusterCount = le32(b[92:])
	v.fatBits = 32
	v.eoc = 0xFFFFFFF8
	if v.clusterCount == 0 || v.ClusterSize > 32<<20 {
		return errors.New("damaged exFAT boot sector")
	}
	v.fat = make([]byte, (int64(v.clusterCount)+2)*4)
	n, _ := v.dev.ReadAt(v.fat, int64(le32(b[80:]))*bps)
	v.fat = v.fat[:n]

	root, _ := v.chain(le32(b[96:]), 1<<16)
	v.rootData = v.readRuns(toRuns(root), 0)
	for i := 0; i+32 <= len(v.rootData); i += 32 {
		e := v.rootData[i : i+32]
		switch e[0] {
		case 0x81: // allocation bitmap
			first, size := le32(e[20:]), int64(le64(e[24:]))
			runs := v.contiguous(first, size)
			need := int(runs[0].Count)
			if cs, ok := v.chain(first, need+1); ok && len(cs) == need {
				runs = toRuns(cs)
			}
			v.bitmap = v.readRuns(runs, size)
		case 0x83: // volume label
			n := min(int(e[1]), 11)
			v.Label = decodeUTF16(e[2 : 2+2*n])
		}
	}
	return nil
}

func decodeUTF16(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		c := le16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

// walkExFAT lists one directory. Deleted entry sets keep their names,
// sizes and first cluster; only the in-use bit of each type byte is cleared.
func (v *Volume) walkExFAT(data []byte, dir string, parentDeleted bool, depth int) {
	if v.Stop.Load() {
		return
	}
	for i := 0; i+32 <= len(data); {
		t := data[i]
		if t == 0x00 {
			// End of a live folder. A deleted folder may have had its start
			// overwritten by other data, so keep looking past zeros there.
			if !parentDeleted {
				break
			}
			i += 32
			continue
		}
		sc := int(data[i+1])
		if (t != 0x85 && t != 0x05) || sc < 2 || sc > 18 || i+32*(sc+1) > len(data) ||
			data[i+32]&0x7F != 0x40 {
			i += 32
			continue
		}
		set := data[i : i+32*(sc+1)]
		i += 32 * (sc + 1)

		deleted := t == 0x05 || parentDeleted
		attr := le16(set[4:])
		mtime := dosTime(le16(set[14:]), le16(set[12:]))
		st := set[32:64]
		noFatChain := st[1]&2 != 0
		nameLen := int(st[3])
		first := le32(st[20:])
		size := int64(le64(st[24:]))

		var nb []byte
		for k := 2; k <= sc; k++ {
			e := set[32*k:]
			if e[0]&0x7F != 0x41 {
				break
			}
			nb = append(nb, e[2:32]...)
		}
		if nameLen*2 > len(nb) {
			continue
		}
		name := decodeUTF16(nb[:nameLen*2])
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") || !v.valid(first) {
			continue
		}
		p := dir + "/" + name

		var runs []Run
		need := int((size + v.ClusterSize - 1) / v.ClusterSize)
		if noFatChain || need == 0 {
			runs = v.contiguous(first, size)
		} else if cs, ok := v.chain(first, need+1); ok && len(cs) == need {
			runs = toRuns(cs)
		} else {
			runs = v.contiguous(first, size)
		}

		if attr&0x10 != 0 {
			if v.visited[first] || depth > 64 {
				continue
			}
			v.visited[first] = true
			v.markKnown(runs)
			sub := v.readRuns(runs, size)
			v.walkExFAT(sub, p, deleted, depth+1)
			continue
		}
		if deleted {
			v.addEntry(p, size, mtime, runs)
		}
	}
}
