// Grok Pane desktop client. Talks to the pane server over HTTP + WS.
package main

import (
	"context"
	"os"
	goruntime "runtime"

	"github.com/jgrant27/pane/web"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func main() {
	app := NewApp()

	appMenu := menu.NewMenu()
	if goruntime.GOOS == "darwin" {
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
	goMenu := appMenu.AddSubmenu("Go")
	goMenu.AddText("Previous Session", keys.Combo("[", keys.CmdOrCtrlKey, keys.ShiftKey), func(_ *menu.CallbackData) {
		app.PrevSession()
	})
	goMenu.AddText("Next Session", keys.Combo("]", keys.CmdOrCtrlKey, keys.ShiftKey), func(_ *menu.CallbackData) {
		app.NextSession()
	})
	goMenu.AddText("Previous Project", keys.Combo("left", keys.CmdOrCtrlKey, keys.OptionOrAltKey), func(_ *menu.CallbackData) {
		app.PrevProject()
	})
	goMenu.AddText("Next Project", keys.Combo("right", keys.CmdOrCtrlKey, keys.OptionOrAltKey), func(_ *menu.CallbackData) {
		app.NextProject()
	})
	file.AddText("Show Project", keys.Combo("o", keys.CmdOrCtrlKey, keys.ShiftKey), func(_ *menu.CallbackData) {
		app.Reveal("")
	})
	file.AddSeparator()
	file.AddText("Connect to pane…", nil, func(_ *menu.CallbackData) {
		if app.ctx != nil {
			runtime.EventsEmit(app.ctx, "request-connect-pane")
		}
	})
	file.AddText("Use local pane", nil, func(_ *menu.CallbackData) {
		go app.SetPaneOrigin("local")
	})
	if goruntime.GOOS != "darwin" {
		file.AddSeparator()
		file.AddText("Quit", keys.CmdOrCtrl("q"), func(_ *menu.CallbackData) {
			os.Exit(0)
		})
	}
	appMenu.Append(menu.EditMenu())

	err := wails.Run(&options.App{
		Title:            "Grok Pane",
		Width:            1040,
		Height:           680,
		MinWidth:         800,
		MinHeight:        520,
		Menu:             appMenu,
		BackgroundColour: &options.RGBA{R: 244, G: 241, B: 234, A: 255},
		AssetServer:      &assetserver.Options{Assets: web.FS},
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop:     true,
			DisableWebViewDrop: true,
		},
		OnStartup:  app.startup,
		OnDomReady: func(ctx context.Context) {
			// macOS restores the last frame; force the smaller default.
			runtime.WindowSetSize(ctx, 1040, 680)
		},
		OnShutdown: app.shutdown,
		Bind:       []interface{}{app},
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
