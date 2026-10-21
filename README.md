# DataRecovery

Desktop app for Windows and macOS that recovers deleted photos, videos and files from SD cards, USB drives and camera cards, keeping their original names, folders and dates.

## Features

- **Original names and folders.** Reads the FAT32/exFAT directory entries of deleted files and folders, including folders whose entries are split across the card.
- **Deep scan.** Finds photos and videos whose names are gone (for example after formatting) by their content: JPEG, PNG, HEIC, MP4, MOV, 3GP.
- **Preview before recovery.** Thumbnails and full previews of deleted photos, read straight from the card.
- **Safe.** Only reads the card, and refuses to save recovered files onto the card being recovered.
- **English and Nepali** interface.

## Download

Installers are built into `dist/`:

| File | Platform |
|---|---|
| `DataRecovery-Setup.exe` | Windows 10/11, 64-bit |
| `DataRecovery.dmg` | macOS 11+, Apple Silicon and Intel |

Step-by-step instructions (in Nepali) are in [README.txt](README.txt) for Windows and [README-mac.txt](README-mac.txt) for macOS.

## Build

Run on a Mac, with Go 1.22+, the Wails CLI and NSIS:

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@v2.10.1
brew install makensis
scripts/build-installers.sh
```

Command-line mode, for scripting:

```sh
DataRecovery -src /dev/rdisk4s1 -out ~/Desktop/Recovered [-deep] [-only /DCIM/Camera]
```

Tests (`DATARECOVERY_TEST_IMAGE` is a FAT or exFAT image containing a deleted folder):

```sh
DATARECOVERY_TEST_IMAGE=card.img go test ./...
```

## Project layout

| Path | What it is |
|---|---|
| `device.go`, `volume.go`, `exfat.go`, `fat.go` | Read-only disk access and FAT/exFAT parsing |
| `carve.go` | Deep scan: lost folder pieces and content-based recovery |
| `recover.go` | Writing recovered files with their folder structure and dates |
| `app.go`, `ui.html` | Interface state and the interface itself |
| `desktop.go` | Native window (Wails) |
| `platform_*.go` | Drive lists and device access for Windows and macOS |
| `build/` | App icon, Windows manifest and installer script, macOS Info.plist |

---

Built by **Arun Pandey**, **Arun AI Studio**, [arun-pandey.com.np](https://arun-pandey.com.np/)

© 2026 Arun Pandey. All rights reserved.
