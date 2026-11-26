// Command qawork-gui opens the QAWork workbench: a native MyGo window with
// the PerfDog-style performance mode as its default.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/neko233-com/QAWork/internal/app"
	"github.com/neko233-com/QAWork/internal/version"
)

type host struct {
	win *mygo.Window
}

func (h *host) Update(fn func()) {
	if h.win != nil {
		h.win.Update(fn)
	}
}

func main() {
	capture := flag.String("capture", "", "把首帧渲染结果写成 PNG 后退出（自检用）")
	demo := flag.Bool("demo", false, "自检：自动采一段再截图")
	demoSerial := flag.String("demo-serial", "local", "自检设备序列号，local 为本机进程")
	demoPackage := flag.String("demo-package", "qawork-gui.exe", "自检目标包名或进程名")
	setupOnly := flag.Bool("setup", false, "直接打开环境安装界面")
	modeFlag := flag.String("mode", "perf", "截图/启动时的工作模式：perf、compat、automation")
	flag.Parse()

	h := &host{}
	a := app.New(h)

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     version.Product + " — 游戏 QA 工作台",
			Width:     1440,
			Height:    900,
			MinWidth:  1100,
			MinHeight: 700,
			Content:   ui.View(a.View),
		})
		h.win = win
		a.ShowSetup = *setupOnly
		switch *modeFlag {
		case "compat", "1":
			a.Mode = app.ModeCompat
		case "automation", "2":
			a.Mode = app.ModeAutomation
		}
		go a.RefreshSetup(context.Background())
		go a.RefreshDevices(context.Background())

		if *demo && !*setupOnly {
			a.Serial = *demoSerial
			a.Target = *demoPackage
			go func() {
				time.Sleep(800 * time.Millisecond)
				a.Start()
			}()
		}

		if *capture != "" {
			wait := 1800 * time.Millisecond
			if *demo && !*setupOnly {
				wait = 9 * time.Second
			}
			go func() {
				time.Sleep(wait)
				png, err := win.CapturePage()
				if err != nil {
					log.Printf("capture: %v", err)
					os.Exit(1)
				}
				if err := os.WriteFile(*capture, png, 0o644); err != nil {
					log.Printf("capture write: %v", err)
					os.Exit(1)
				}
				fmt.Println("captured", *capture)
				os.Exit(0)
			}()
		}
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
