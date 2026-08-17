// Grok Pane desktop client. Talks to the pane server over HTTP + WS.
package main

import (
	"os"
	"runtime"

	"github.com/jgrant27/pane/web"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

func main() {
	app := NewApp()

	appMenu := menu.NewMenu()
	if runtime.GOOS == "darwin" {
		appMenu.Append(menu.AppMenu())
	}
	file := appMenu.AddSubmenu("File")
	file.AddText("Open Project…", keys.CmdOrCtrl("o"), func(_ *menu.CallbackData) {
		// Dialog must not run on the Cocoa main thread — Wails would deadlock.
		go app.OpenProject()
	})
	file.AddText("New Session", keys.CmdOrCtrl("n"), func(_ *menu.CallbackData) {
		app.NewSession()
	})
	file.AddText("Close Session", keys.CmdOrCtrl("w"), func(_ *menu.CallbackData) {
		app.CloseSession()
	})
	file.AddText("Show Project", keys.Combo("o", keys.CmdOrCtrlKey, keys.ShiftKey), func(_ *menu.CallbackData) {
		app.Reveal("")
	})
	if runtime.GOOS != "darwin" {
		file.AddSeparator()
		file.AddText("Quit", keys.CmdOrCtrl("q"), func(_ *menu.CallbackData) {
			os.Exit(0)
		})
	}
	appMenu.Append(menu.EditMenu())

	err := wails.Run(&options.App{
		Title:            "Grok Pane",
		Width:            1240,
		Height:           820,
		MinWidth:         880,
		MinHeight:        560,
		Menu:             appMenu,
		BackgroundColour: &options.RGBA{R: 244, G: 241, B: 234, A: 255},
		AssetServer:      &assetserver.Options{Assets: web.FS},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []interface{}{app},
		Mac: &mac.Options{
			TitleBar: mac.TitleBarDefault(),
			About: &mac.AboutInfo{
				Title:   "Grok Pane",
				Message: "Desktop face for grok agent serve.\nTalks to the local pane server.",
				Icon:    icon,
			},
		},
		Windows: &windows.Options{},
		Linux: &linux.Options{
			Icon: icon,
		},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
