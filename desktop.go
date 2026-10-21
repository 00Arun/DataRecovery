package main

import (
	"context"
	goruntime "runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	Author    = "Arun Pandey"
	Company   = "Arun AI Studio"
	Website   = "https://arun-pandey.com.np/"
	Copyright = "© 2026 Arun Pandey. All rights reserved."
)

// runDesktop shows the app in its own native window. The interface and its
// requests are served in-process by the window's asset handler.
func runDesktop() error {
	var ctx context.Context
	a := &app{phase: "idle"}
	a.pickFolder = func() string {
		dir, _ := runtime.OpenDirectoryDialog(ctx, runtime.OpenDialogOptions{
			Title:                "Choose where to save recovered files (not the card being recovered)",
			DefaultDirectory:     defaultOut(),
			CanCreateDirectories: true,
		})
		return dir
	}
	a.openWebsite = func() { runtime.BrowserOpenURL(ctx, Website) }

	// macOS apps get the standard menu bar (with About DataRecovery);
	// Windows keeps a plain window.
	var appMenu *menu.Menu
	if goruntime.GOOS == "darwin" {
		appMenu = menu.NewMenuFromItems(menu.AppMenu(), menu.EditMenu(), menu.WindowMenu())
		help := appMenu.AddSubmenu("Help")
		help.AddText("Visit arun-pandey.com.np", nil, func(*menu.CallbackData) { a.openWebsite() })
	}
	return wails.Run(&options.App{
		Menu:             appMenu,
		Title:            "DataRecovery",
		Width:            1240,
		Height:           860,
		MinWidth:         880,
		MinHeight:        620,
		BackgroundColour: &options.RGBA{R: 244, G: 246, B: 250, A: 255},
		AssetServer:      &assetserver.Options{Handler: newMux(a)},
		OnStartup:        func(c context.Context) { ctx = c },
		Windows:          &windows.Options{Theme: windows.SystemDefault},
		Mac: &mac.Options{
			About: &mac.AboutInfo{
				Title: "DataRecovery",
				Message: "Recover deleted photos, videos and files from SD cards and USB drives.\n\n" +
					"Built by " + Author + ", " + Company + "\n" + Website + "\n\n" + Copyright,
			},
		},
	})
}
