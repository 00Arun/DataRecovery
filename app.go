package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

//go:embed ui.html
var uiHTML []byte

// app holds the state behind the user interface. The interface talks to it
// over HTTP-style requests that the desktop window serves in-process; no
// network port is opened.
type app struct {
	pickFolder  func() string // native folder dialog, set by the desktop shell
	openWebsite func()        // opens the author's website in the system browser
	mu          sync.Mutex
	dev         *Device
	vol         *Volume
	source      string
	phase       string // idle, scanning, ready, recovering, done, error
	msg         string
	done        int64
	total       int64
	result      *RecoverResult
}

func newMux(a *app) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(uiHTML)
	})
	api := func(path string, h func(r *http.Request) (any, error)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			res, err := h(r)
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				res = map[string]string{"error": err.Error()}
			}
			json.NewEncoder(w).Encode(res)
		})
	}
	api("/api/drives", func(*http.Request) (any, error) { return listDrives(), nil })
	api("/api/status", a.status)
	api("/api/entries", a.entries)
	api("/api/scan", a.scan)
	api("/api/recover", a.recover)
	api("/api/cancel", a.cancel)
	api("/api/pickfolder", func(*http.Request) (any, error) {
		p := ""
		if a.pickFolder != nil {
			p = a.pickFolder()
		}
		return map[string]string{"path": p}, nil
	})
	api("/api/website", func(*http.Request) (any, error) {
		if a.openWebsite != nil {
			a.openWebsite()
		}
		return nil, nil
	})
	api("/api/open", func(r *http.Request) (any, error) {
		var req struct{ Path string }
		json.NewDecoder(r.Body).Decode(&req)
		openFolder(req.Path)
		return nil, nil
	})
	mux.HandleFunc("/api/thumb", func(w http.ResponseWriter, r *http.Request) { a.serveImage(w, r, true) })
	mux.HandleFunc("/api/file", func(w http.ResponseWriter, r *http.Request) { a.serveImage(w, r, false) })
	return mux
}

// serveImage sends a photo's embedded thumbnail (thumb) or the whole photo,
// read straight from the drive, so files can be previewed before recovery.
func (a *app) serveImage(w http.ResponseWriter, r *http.Request, thumb bool) {
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	a.mu.Lock()
	v := a.vol
	busy := a.phase == "scanning"
	a.mu.Unlock()
	if v == nil || busy || id < 0 || id >= len(v.Entries) {
		http.NotFound(w, r)
		return
	}
	e := v.Entries[id]
	ext := strings.ToLower(path.Ext(e.Path))
	var data []byte
	switch {
	case ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".gif" && ext != ".bmp" && ext != ".webp":
	case thumb && (ext == ".jpg" || ext == ".jpeg"):
		if data = exifThumb(v.Head(e, 128<<10)); data == nil && e.Size <= 12<<20 {
			data = v.Head(e, e.Size)
		}
	case e.Size <= 40<<20:
		data = v.Head(e, e.Size)
	}
	if len(data) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Write(data)
}

func (a *app) progress(msg string, done, total int64) {
	a.mu.Lock()
	a.msg, a.done, a.total = msg, done, total
	a.mu.Unlock()
}

func (a *app) status(*http.Request) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := map[string]any{"phase": a.phase, "msg": a.msg, "done": a.done, "total": a.total,
		"source": a.source, "result": a.result, "defaultOut": defaultOut()}
	if a.vol != nil {
		s["fs"], s["label"], s["count"] = a.vol.Kind, a.vol.Label, len(a.vol.Entries)
	}
	return s, nil
}

func (a *app) entries(*http.Request) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.vol == nil || a.phase == "scanning" {
		return []*Entry{}, nil
	}
	return a.vol.Entries, nil
}

func (a *app) busy() bool { return a.phase == "scanning" || a.phase == "recovering" }

func (a *app) scan(r *http.Request) (any, error) {
	var req struct {
		Source string
		Deep   bool
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Source == "" {
		return nil, errors.New("choose a drive first")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy() {
		return nil, errors.New("please wait for the current task to finish")
	}
	if a.dev != nil {
		a.dev.Close()
		a.dev, a.vol = nil, nil
	}
	a.source, a.phase, a.msg, a.done, a.total, a.result = req.Source, "scanning", "Opening drive", 0, 0, nil
	go func() {
		prepareSource(req.Source)
		dev, err := OpenDevice(req.Source)
		var v *Volume
		if err == nil {
			if v, err = OpenVolume(dev); err != nil {
				dev.Close()
			}
		}
		if err != nil {
			a.mu.Lock()
			a.phase, a.msg = "error", friendlyError(err)
			a.mu.Unlock()
			return
		}
		v.Progress = a.progress
		a.mu.Lock()
		a.dev, a.vol = dev, v
		a.mu.Unlock()
		v.Scan()
		if req.Deep {
			v.Carve()
		}
		a.mu.Lock()
		a.phase, a.msg = "ready", ""
		if v.Stop.Load() {
			a.msg = "Scan stopped early; showing what was found so far."
		}
		v.Stop.Store(false)
		a.mu.Unlock()
	}()
	return nil, nil
}

func friendlyError(err error) string {
	if errors.Is(err, os.ErrPermission) {
		return permissionHelp
	}
	return err.Error()
}

func (a *app) recover(r *http.Request) (any, error) {
	var req struct {
		IDs []int
		Out string
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy() || a.vol == nil {
		return nil, errors.New("scan a drive first")
	}
	if len(req.IDs) == 0 {
		return nil, errors.New("select at least one file")
	}
	out, err := filepath.Abs(req.Out)
	if err != nil || req.Out == "" {
		return nil, errors.New("choose a folder to save the recovered files in")
	}
	if sameDrive(a.source, out) {
		return nil, errors.New("save the files on a different drive. Writing to the drive being recovered would destroy the files you are trying to get back")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	v := a.vol
	a.phase, a.result = "recovering", nil
	go func() {
		res := v.Recover(req.IDs, out)
		a.mu.Lock()
		a.phase, a.result = "done", &res
		v.Stop.Store(false)
		a.mu.Unlock()
	}()
	return nil, nil
}

func (a *app) cancel(*http.Request) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.vol != nil {
		a.vol.Stop.Store(true)
	}
	return nil, nil
}
