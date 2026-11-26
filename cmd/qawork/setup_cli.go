package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/neko233-com/QAWork/internal/setup"
)

// cmdSetup is the agent/CI face of the environment installer. The GUI
// screen calls the same setup package; this command must stay scriptable.
func cmdSetup(args []string) int {
	action := "check"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = args[0]
		args = args[1:]
	}
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	ids := fs.String("id", "", "要安装的依赖，逗号分隔：adb,scrcpy")
	asJSON := fs.Bool("json", false, "输出 JSON")
	noPath := fs.Bool("no-path", false, "不写当前用户 PATH（测试/便携模式）")
	timeout := fs.Duration("timeout", 10*time.Minute, "安装超时")
	fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	switch action {
	case "check", "status":
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		report := setup.Inspect(ctx)
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(report)
		} else {
			fmt.Printf("工具目录: %s\n", report.ToolsDir)
			for _, d := range report.Deps {
				state := map[setup.Status]string{
					setup.StatusOK: "已安装", setup.StatusMissing: "未安装", setup.StatusManual: "需手动",
				}[d.Status]
				req := "可选"
				if d.Required {
					req = "必装"
				}
				line := fmt.Sprintf("[%s] %s（%s）", state, d.Name, req)
				if d.Version != "" {
					line += " " + d.Version
				}
				if d.Path != "" {
					line += " " + d.Path
				}
				fmt.Println(line)
				if d.Status != setup.StatusOK && d.Note != "" {
					fmt.Println("      " + d.Note)
				}
			}
			fmt.Println(report.Summary)
		}
		for _, d := range report.Deps {
			if d.Required && d.Status == setup.StatusMissing {
				return 2
			}
		}
		return 0
	case "install":
		list := []string{"adb"}
		if *ids != "" {
			list = nil
			for _, id := range strings.Split(*ids, ",") {
				if id = strings.TrimSpace(id); id != "" {
					list = append(list, id)
				}
			}
		}
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		results := make([]setup.Dep, 0, len(list))
		for _, id := range list {
			progress := func(msg string) { fmt.Fprintln(os.Stderr, msg) }
			dep, err := setup.Install(ctx, id, !*noPath, progress)
			if err != nil {
				if *asJSON {
					json.NewEncoder(os.Stderr).Encode(map[string]any{"id": id, "error": err.Error()})
				}
				fmt.Fprintf(os.Stderr, "安装 %s 失败: %v\n", id, err)
				return 1
			}
			results = append(results, dep)
		}
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(map[string]any{"installed": results})
		} else {
			for _, d := range results {
				fmt.Printf("已安装 %s %s\n", d.Name, d.Path)
			}
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "未知 setup 动作 %q（check / install）\n", action)
		return 1
	}
}
