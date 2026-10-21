package main

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
)

// Device reads a volume, a whole disk or a disk image. It never writes.
// Raw devices only accept sector-aligned reads, so every read is widened
// to an aligned window and the requested slice is copied out of it.
type Device struct {
	f    *os.File
	base int64 // byte offset of the filesystem inside the device
}

func OpenDevice(path string) (*Device, error) {
	f, err := openRaw(path)
	if err != nil {
		return nil, err
	}
	d := &Device{f: f}
	if err := d.findFilesystem(); err != nil {
		f.Close()
		return nil, err
	}
	return d, nil
}

func (d *Device) Close() error { return d.f.Close() }

func (d *Device) ReadAt(p []byte, off int64) (int, error) {
	n, err := d.readAligned(p, d.base+off, 4096)
	if n == 0 && err != nil {
		n, err = d.readAligned(p, d.base+off, 512)
	}
	return n, err
}

func (d *Device) readAligned(p []byte, off, align int64) (int, error) {
	start := off / align * align
	end := (off + int64(len(p)) + align - 1) / align * align
	buf := make([]byte, end-start)
	n, err := d.f.ReadAt(buf, start)
	skip := off - start
	if int64(n) <= skip {
		if err == nil {
			err = io.EOF
		}
		return 0, err
	}
	c := copy(p, buf[skip:n])
	if c < len(p) {
		if err == nil {
			err = io.EOF
		}
		return c, err
	}
	return c, nil
}

// bootKind reports which filesystem a boot sector belongs to, or "".
func bootKind(b []byte) string {
	if len(b) < 512 {
		return ""
	}
	if string(b[3:11]) == "EXFAT   " {
		return "exFAT"
	}
	if b[510] != 0x55 || b[511] != 0xAA || (b[0] != 0xEB && b[0] != 0xE9) {
		return ""
	}
	bps := binary.LittleEndian.Uint16(b[11:])
	spc := b[13]
	if (bps != 512 && bps != 1024 && bps != 2048 && bps != 4096) ||
		spc == 0 || spc&(spc-1) != 0 || b[16] == 0 || b[16] > 2 ||
		binary.LittleEndian.Uint16(b[14:]) == 0 {
		return ""
	}
	return "FAT"
}

// findFilesystem accepts a bare volume, or an MBR/GPT disk whose first
// FAT/exFAT partition is then used.
func (d *Device) findFilesystem() error {
	b := make([]byte, 512)
	if _, err := d.ReadAt(b, 0); err != nil {
		return err
	}
	if bootKind(b) != "" {
		return nil
	}
	if b[510] == 0x55 && b[511] == 0xAA {
		for i := 0; i < 4; i++ {
			e := b[446+16*i:]
			lba := int64(binary.LittleEndian.Uint32(e[8:]))
			if e[4] == 0xEE {
				if d.findGPT() {
					return nil
				}
				continue
			}
			if e[4] != 0 && lba != 0 && d.tryBase(lba*512) {
				return nil
			}
		}
	}
	return errors.New("no FAT32 or exFAT file system found on this drive")
}

func (d *Device) findGPT() bool {
	h := make([]byte, 512)
	if _, err := d.ReadAt(h, 512); err != nil || string(h[:8]) != "EFI PART" {
		return false
	}
	lba := int64(binary.LittleEndian.Uint64(h[72:]))
	count := int(binary.LittleEndian.Uint32(h[80:]))
	size := int(binary.LittleEndian.Uint32(h[84:]))
	if size < 128 || count > 256 {
		return false
	}
	tbl := make([]byte, count*size)
	if _, err := d.ReadAt(tbl, lba*512); err != nil {
		return false
	}
	for i := 0; i < count; i++ {
		first := int64(binary.LittleEndian.Uint64(tbl[i*size+32:]))
		if first != 0 && d.tryBase(first*512) {
			return true
		}
	}
	return false
}

func (d *Device) tryBase(base int64) bool {
	old := d.base
	d.base = base
	b := make([]byte, 512)
	if _, err := d.ReadAt(b, 0); err == nil && bootKind(b) != "" {
		return true
	}
	d.base = old
	return false
}
