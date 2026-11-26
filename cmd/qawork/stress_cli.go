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

	"github.com/neko233-com/QAWork/internal/perf"
	"github.com/neko233-com/QAWork/internal/setup"
	"github.com/neko233-com/QAWork/internal/stress"
)

func cmdStress(args []string) int {
	fs := flag.NewFlagSet("stress", flag.ExitOnError)
	pkg := fs.String("package", "", "包名")
	serial := fs.String("serial", "", "单台设备；留空取第一台在线")
	devicesFlag := fs.String("devices", "", "逗号分隔多台设备；优先于 -serial")
	adbPath := fs.String("adb", "", "adb 可执行文件")
	duration := fs.Duration("duration", 5*time.Minute, "最长压力时长")
	events := fs.Int("events", 3000, "monkey 事件数")
	throttle := fs.Int("throttle", 300, "事件间隔 ms")
	interval := fs.Duration("interval", time.Second, "采样间隔")
	artifacts := fs.String("artifacts", "", "产物根目录")
	asJSON := fs.Bool("json", false, "输出 JSON")
	fs.Parse(args)
	if *pkg == "" {
		fmt.Fprintln(os.Stderr, "缺少 -package")
		return 1
	}
	adb := perf.ADB{Path: *adbPath}
	if adb.Path == "" {
		adb.Path = setup.ADBPath()
	}
	root := *artifacts
	if root == "" {
		root = filepath.Join(setup.HomeDir(), "stress", time.Now().Format("20060102-150405"))
	}
	serials := splitCSV(*devicesFlag)
	if len(serials) == 0 && *serial != "" {
		serials = []string{*serial}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var results []stress.Result
	if len(serials) == 0 {
		res, err := stress.Run(ctx, stress.Options{
			ADB: adb, Serial: *serial, Package: *pkg, Duration: *duration,
			Events: *events, ThrottleMS: *throttle, Interval: *interval, ArtifactsRoot: root,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "压力测试失败:", err)
			return 1
		}
		results = append(results, res)
	} else {
		for _, s := range serials {
			fmt.Fprintf(os.Stderr, "[%s] 压力测试 %s ...\n", s, *pkg)
			res, err := stress.Run(ctx, stress.Options{
				ADB: adb, Serial: s, Package: *pkg, Duration: *duration,
				Events: *events, ThrottleMS: *throttle, Interval: *interval,
				ArtifactsRoot: filepath.Join(root, safeDeviceName(s)),
			})
			if err != nil {
				res = stress.Result{Serial: s, Package: *pkg, Failed: true, Error: err.Error()}
			}
			results = append(results, res)
		}
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"package": *pkg, "results": results, "failed": stressFailed(results)})
	} else {
		fmt.Println("SERIAL\tPACKAGE\tSAMPLES\tFPS\tCRASH\tANR\tPASS\tERROR")
		for _, r := range results {
			fmt.Printf("%s\t%s\t%d\t%.1f\t%v\t%v\t%v\t%s\n",
				r.Serial, r.Package, r.Samples, r.Summary.FPSMean, r.Crash, r.ANR, !r.Failed, r.Error)
			if r.ResultJSON != "" {
				fmt.Println("  产物:", r.ResultJSON)
			}
		}
	}
	if stressFailed(results) {
		return 2
	}
	return 0
}

func stressFailed(results []stress.Result) bool {
	for _, r := range results {
		if r.Failed {
			return true
		}
	}
	return false
}

func safeDeviceName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-.")
}
