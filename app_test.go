package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWebFlow drives the UI's API end to end: scan an image, list the
// deleted files, recover them and check the folder structure on disk.
// Set DATARECOVERY_TEST_IMAGE to a FAT or exFAT image with a deleted folder.
func TestWebFlow(t *testing.T) {
	img := os.Getenv("DATARECOVERY_TEST_IMAGE")
	if img == "" {
		t.Skip("DATARECOVERY_TEST_IMAGE not set")
	}
	srv := httptest.NewServer(newMux(&app{phase: "idle"}))
	defer srv.Close()
	call := func(path string, body any, out any) int {
		var rd *bytes.Reader
		method := "GET"
		if body != nil {
			b, _ := json.Marshal(body)
			rd, method = bytes.NewReader(b), "POST"
		} else {
			rd = bytes.NewReader(nil)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if out != nil {
			json.NewDecoder(res.Body).Decode(out)
		}
		return res.StatusCode
	}
	wait := func(phase string) map[string]any {
		for i := 0; i < 600; i++ {
			var s map[string]any
			call("/api/status", nil, &s)
			if s["phase"] == phase {
				return s
			}
			if s["phase"] == "error" {
				t.Fatalf("error: %v", s["msg"])
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", phase)
		return nil
	}

	if code := call("/api/scan", map[string]any{"Source": img, "Deep": true}, nil); code != 200 {
		t.Fatalf("scan: %d", code)
	}
	wait("ready")
	var entries []Entry
	call("/api/entries", nil, &entries)
	if len(entries) == 0 {
		t.Fatal("no entries")
	}
	var ids []int
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	out := t.TempDir()
	if code := call("/api/recover", map[string]any{"IDs": ids, "Out": out}, nil); code != 200 {
		t.Fatalf("recover: %d", code)
	}
	s := wait("done")
	res := s["result"].(map[string]any)
	if int(res["ok"].(float64)) != len(entries) {
		t.Fatalf("recovered %v of %d", res["ok"], len(entries))
	}
	n := 0
	filepath.Walk(res["root"].(string), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n++
		}
		return nil
	})
	if n != len(entries) {
		t.Fatalf("%d files on disk, want %d", n, len(entries))
	}
	if code := call("/api/drives", nil, nil); code != 200 {
		t.Fatalf("drives: %d", code)
	}
}
