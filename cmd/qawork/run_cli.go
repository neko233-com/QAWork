package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/neko233-com/QAWork/internal/automation"
	"github.com/neko233-com/QAWork/internal/perf"
	"github.com/neko233-com/QAWork/internal/setup"
	"github.com/neko233-com/QAWork/internal/version"
)

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	planPath := fs.String("plan", "qawork.yaml", "qawork.yaml 路径")
	caseName := fs.String("case", "", "只跑指定 case；默认全部")
	validateOnly := fs.Bool("validate", false, "只校验计划，不碰设备")
	listOnly := fs.Bool("list", false, "列出 case")
	asJSON := fs.Bool("json", false, "输出 JSON")
	adbPath := fs.String("adb", "", "adb 可执行文件，默认自动找")
	serial := fs.String("serial", "", "覆盖所有 case 的设备序列号")
	artifacts := fs.String("artifacts", "", "产物根目录")
	timeout := fs.Duration("timeout", 10*time.Minute, "单个 case 超时")
	fs.Parse(args)

	plan, err := automation.LoadPlan(*planPath)
	if err != nil {
		var verr *automation.ValidationError
		if errors.As(err, &verr) {
			fmt.Fprintln(os.Stderr, "计划无效:")
			for _, e := range verr.Errors {
				fmt.Fprintln(os.Stderr, "  -", e)
			}
		} else {
			fmt.Fprintln(os.Stderr, "读取计划失败:", err)
		}
		return 1
	}

	if *validateOnly || *listOnly {
		if *asJSON {
			json.NewEncoder(os.Stdout).Encode(map[string]any{
				"plan": *planPath, "version": plan.Version, "cases": plan.Names(), "valid": true,
			})
			return 0
		}
		fmt.Printf("计划 %s：%d 个 case\n", *planPath, len(plan.Cases))
		for i, c := range plan.Cases {
			fmt.Printf("  [%d] %s  %s  %d 步\n", i, c.Name, c.Package, len(c.Steps))
		}
		return 0
	}

	indices, err := automation.SelectIndices(plan, *caseName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	adb := perf.ADB{Path: *adbPath}
	if adb.Path == "" {
		adb.Path = setup.ADBPath()
	}
	root := *artifacts
	if root == "" {
		root = filepath.Join(setup.HomeDir(), "automation", time.Now().Format("20060102-150405"))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var started []string
	results := automation.Execute(ctx, automation.ExecuteOptions{
		ADB: adb, Plan: plan, Indices: indices, Serial: *serial,
		ArtifactsRoot: root, Timeout: *timeout,
		OnStart: func(idx int, c automation.Case) {
			started = append(started, c.Name)
			fmt.Fprintf(os.Stderr, "[%d/%d] %s ...\n", len(started), len(indices), c.Name)
		},
	})

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{
			"version": version.Version,
			"plan":    *planPath,
			"results": results,
			"failed":  anyFailed(results),
		})
	} else {
		for _, r := range results {
			state := "通过"
			if r.Failed {
				state = "未通过"
			}
			fmt.Printf("[%s] %s", state, r.Case)
			if r.Error != "" {
				fmt.Printf(" — %s", r.Error)
			} else {
				fmt.Printf(" fps=%.1f jank=%.1f%% cpu=%.0f%% mem=%.0fMB", r.Summary.FPSMean, r.Summary.JankPct, r.Summary.CPUMax, r.Summary.MemMaxMB)
				if r.StartupMS > 0 {
					fmt.Printf(" startup=%.0fms", r.StartupMS)
				}
			}
			fmt.Println()
			for _, c := range r.Checks {
				if !c.OK {
					fmt.Printf("    [未通过] %s %.1f（限 %.1f %s）\n", c.Name, c.Value, c.Limit, c.Unit)
				}
			}
			if len(r.Artifacts) > 0 {
				fmt.Printf("    产物: %s\n", filepath.Dir(r.Artifacts[0]))
			}
		}
	}

	if anyError(results) {
		return 1
	}
	if anyFailed(results) {
		return 2
	}
	return 0
}

func anyFailed(results []automation.Result) bool {
	for _, r := range results {
		if r.Failed {
			return true
		}
	}
	return false
}

func anyError(results []automation.Result) bool {
	for _, r := range results {
		if r.Error != "" {
			return true
		}
	}
	return false
}
