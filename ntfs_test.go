package main

import (
	"reflect"
	"testing"
)

func TestDecodeRuns(t *testing.T) {
	// 4 clusters at LCN 0x1000, then 2 clusters 256 clusters back,
	// then a 5-cluster sparse hole, then the end marker.
	b := []byte{0x31, 0x04, 0x00, 0x10, 0x00, 0x21, 0x02, 0x00, 0xFF, 0x01, 0x05, 0x00}
	want := []Run{{0x1000 + 2, 4}, {0x1000 - 256 + 2, 2}, {0, 5}}
	if got := decodeRuns(b); !reflect.DeepEqual(got, want) {
		t.Fatalf("decodeRuns = %v, want %v", got, want)
	}
}

func TestApplyFixups(t *testing.T) {
	b := make([]byte, 1024)
	copy(b, "FILE")
	b[4], b[6] = 48, 3                      // update sequence array at 48, 3 entries
	b[48], b[49] = 0xAB, 0xCD               // sequence number
	b[50], b[51], b[52], b[53] = 1, 2, 3, 4 // original bytes
	b[510], b[511], b[1022], b[1023] = 0xAB, 0xCD, 0xAB, 0xCD
	if !applyFixups(b) || b[510] != 1 || b[511] != 2 || b[1022] != 3 || b[1023] != 4 {
		t.Fatalf("fixups not applied: %x %x", b[510:512], b[1022:1024])
	}
	b[510] = 0
	b[50] = 0xAB
	if applyFixups(append([]byte(nil), b...)) {
		t.Fatal("torn record accepted")
	}
}
