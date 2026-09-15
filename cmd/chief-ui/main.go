// chief-ui is the Wails main-window app for Chief.
//
// It is a client of chiefd (over the same unix socket the CLI uses) and
// renders projects, backlogs, and task detail in a proper macOS window.
// The menu-bar helper (cmd/chief-menu) launches this window on demand and
// the two coexist as separate .app bundles.
package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

// Version is stamped at build time via -ldflags.
var Version = "dev"

func main() {
	app := NewApp()
	err := wails.Run(&options.App{
		Title:            "Chief",
		Width:            1200,
		Height:           760,
		MinWidth:         900,
		MinHeight:        520,
		BackgroundColour: &options.RGBA{R: 20, G: 20, B: 24, A: 1},
		OnStartup:        app.startup,
		Bind:             []interface{}{app},
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			About: &mac.AboutInfo{
				Title:   "Chief",
				Message: "Multi-project Claude Code orchestrator.\nVersion " + Version,
			},
		},
	})
	if err != nil {
		println("chief-ui error:", err.Error())
	}
}
