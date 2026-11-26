package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/neko233-com/QAWork/internal/automation"
	"github.com/neko233-com/QAWork/internal/compat"
	"github.com/neko233-com/QAWork/internal/perf"
	"github.com/neko233-com/QAWork/internal/setup"
)

func cmdCompat(args []string) int {
	fs := flag.NewFlagSet("compat", flag.ExitOnError)
	planPath := fs.String("plan", "", "可选 qawork.yaml；不填时只做启动 smoke")
	caseName := fs.String("case", "", "只跑指定 case")
	apkPath := fs.String("apk", "", "可选 APK，先批量安装")
	pkg := fs.String("package", "", "无 plan 时的包名")
	devicesFlag := fs.String("devices", "", "设备序列号，逗号分隔；留空为全部在线设备")
	artifacts := fs.String("artifacts", "", "产物根目录")
	adbPath := fs.String("adb", "", "adb 可执行文件")
	asJSON := fs.Bool("json", false, "输出 JSON")
	timeout := fs.Duration("timeout", 10*time.Minute, "单个 case 超时")
	fs.Parse(args)

	var plan *automation.Plan
	if *planPath != "" {
		p, err := automation.LoadPlan(*planPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "读取计划失败:", err)
			return 1
		}
		plan = p
	}
	if *apkPath != "" {
		if _, err := os.Stat(*apkPath); err != nil {
			fmt.Fprintln(os.Stderr, "APK 不存在:", err)
			return 1
		}
	}
	var devices []string
	for _, s := range strings.Split(*devicesFlag, ",") {
		if s = strings.TrimSpace(s); s != "" && !strings.EqualFold(s, "all") {
			devices = append(devices, s)
		}
	}
	adb := perf.ADB{Path: *adbPath}
	if adb.Path == "" {
		adb.Path = setup.ADBPath()
	}
	root := *artifacts
	if root == "" {
		root = filepath.Join(setup.HomeDir(), "compat", time.Now().Format("20060102-150405"))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	matrix, err := compat.Run(ctx, compat.Options{
		ADB: adb, Plan: plan, CaseName: *caseName, Package: *pkg, APKPath: *apkPath,
		Devices: devices, ArtifactsRoot: root, Timeout: *timeout,
		OnDevice: func(serial string, done, total int) {
			fmt.Fprintf(os.Stderr, "[%d/%d] %s ...\n", done, total, serial)
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "兼容矩阵失败:", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(matrix)
	} else {
		fmt.Printf("兼容矩阵：%d 台设备 × %d 个用例，%.1fs\n", matrix.Devices, matrix.Cases, matrix.DurationSeconds)
		fmt.Println("SERIAL\tMODEL\tANDROID\tCASE\tINSTALL\tSTARTUP\tFPS\tJANK\tCPU\tMEM\tCRASH\tANR\tPASS")
		for _, r := range matrix.Rows {
			inst := "跳过"
			if !r.InstallSkipped {
				inst = fmt.Sprintf("%v", r.Installed)
			}
			fmt.Printf("%s\t%s\t%s\t%s\t%s\t%.0f\t%.1f\t%.1f%%\t%.0f%%\t%.0fMB\t%v\t%v\t%v\n",
				r.Serial, r.Model, r.Android, r.Case, inst, r.StartupMS, r.FPSMean, r.JankPct, r.CPUMax, r.MemMaxMB, r.Crash, r.ANR, !r.Failed)
			if r.Error != "" {
				fmt.Printf("  error: %s\n", r.Error)
			}
		}
		if matrix.CSV != "" {
			fmt.Println("CSV:", matrix.CSV)
		}
		if matrix.JSON != "" {
			fmt.Println("JSON:", matrix.JSON)
		}
	}
	if matrix.Failed {
		return 2
	}
	return 0
}
