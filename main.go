// DataRecovery recovers deleted photos, videos and other files from FAT and
// exFAT drives (SD cards, USB sticks, camera cards), keeping their original
// names and folder structure where the file system still records them.
// It only reads the source drive.
//
// Built by Arun Pandey, Arun AI Studio - https://arun-pandey.com.np/
// Copyright © 2026 Arun Pandey. All rights reserved.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

type Drive struct {
	Path      string `json:"path"`
	Letter    string `json:"letter"`
	Label     string `json:"label"`
	FS        string `json:"fs"`
	Size      int64  `json:"size"`
	Removable bool   `json:"removable"`
	System    bool   `json:"system"`
}

func main() {
	src := flag.String("src", "", "drive, device or image to scan (command-line mode)")
	out := flag.String("out", "", "folder to recover all found files into (command-line mode)")
	deep := flag.Bool("deep", false, "also search free space for photos and videos without names")
	only := flag.String("only", "", "recover only files whose path contains this text (command-line mode)")
	flag.Parse()

	if *src != "" {
		if err := runCLI(*src, *out, *only, *deep); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if err := runDesktop(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runCLI(src, out, only string, deep bool) error {
	dev, err := OpenDevice(src)
	if err != nil {
		return err
	}
	defer dev.Close()
	v, err := OpenVolume(dev)
	if err != nil {
		return err
	}
	fmt.Printf("%s volume %q, cluster %d bytes, %d clusters\n", v.Kind, v.Label, v.ClusterSize, v.clusterCount)
	v.Scan()
	if deep || v.Kind == "Raw" {
		v.Carve()
	}

	folders := map[string][4]int64{}
	for _, e := range v.Entries {
		dir := e.Path[:strings.LastIndex(e.Path, "/")+1]
		f := folders[dir]
		f[0]++
		f[1] += e.Size
		if e.Status == "good" {
			f[2]++
		}
		folders[dir] = f
	}
	dirs := make([]string, 0, len(folders))
	for d := range folders {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	fmt.Printf("%d deleted files found\n", len(v.Entries))
	for _, d := range dirs {
		f := folders[d]
		fmt.Printf("  %6d files %8.1f MB  (%d intact)  %s\n", f[0], float64(f[1])/1e6, f[2], d)
	}

	if out != "" {
		var ids []int
		for i, e := range v.Entries {
			if strings.Contains(e.Path, only) {
				ids = append(ids, i)
			}
		}
		res := v.Recover(ids, out)
		fmt.Printf("recovered %d files into %s, %d failed\n", res.OK, res.Root, len(res.Failed))
		for _, f := range res.Failed {
			fmt.Println("  failed:", f)
		}
	}
	return nil
}
