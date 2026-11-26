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

	"github.com/neko233-com/QAWork/internal/adbkit"
	"github.com/neko233-com/QAWork/internal/automation"
	"github.com/neko233-com/QAWork/internal/perf"
	"github.com/neko233-com/QAWork/internal/setup"
)

func cmdDevice(args []string) int {
	if len(args) == 0 {
		deviceUsage()
		return 1
	}
	switch args[0] {
	case "info":
		return deviceInfo(args[1:])
	case "install":
		return deviceInstall(args[1:])
	case "uninstall":
		return deviceUninstall(args[1:])
	case "start":
		return deviceStart(args[1:])
	case "stop":
		return deviceStop(args[1:])
	case "clear":
		return deviceClear(args[1:])
	case "grant", "revoke":
		return devicePerm(args[0], args[1:])
	case "screenshot":
		return deviceScreenshot(args[1:])
	case "logcat":
		return deviceLogcat(args[1:])
	case "monkey":
		return deviceMonkey(args[1:])
	case "animations":
		return deviceAnimations(args[1:])
	case "wake":
		return deviceWake(args[1:])
	case "unlock":
		return deviceUnlock(args[1:])
	case "push":
		return devicePush(args[1:])
	case "pull":
		return devicePull(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "未知 device 动作 %q\n\n", args[0])
		deviceUsage()
		return 1
	}
}

func deviceUsage() {
	fmt.Fprintln(os.Stderr, `device 子命令（全部是显式操作，不提供任意 adb shell）:
  info       设备型号/系统/ABI/分辨率/密度/电量
  install    -apk FILE [-replace] [-grant]
  uninstall  -package PKG [-keep]
  start      -package PKG [-activity ACT] [-cold] 
  stop       -package PKG
  clear      -package PKG
  grant      -package PKG -permissions PERM,PERM
  revoke     -package PKG -permissions PERM,PERM
  screenshot [-out FILE]
  logcat     [-out FILE] [-clear] [-lines N]
  monkey     -package PKG [-events N] [-throttle MS]
  animations -state on|off
  wake | unlock
  push       -local FILE -remote PATH
  pull       -remote PATH -local FILE`)
}

func deviceFlags(name string) (*flag.FlagSet, *string, *string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	adbPath := fs.String("adb", "", "adb 可执行文件")
	serial := fs.String("serial", "", "设备序列号；留空取第一台在线设备")
	return fs, adbPath, serial
}

func deviceClient(adbPath *string) adbkit.Client {
	p := *adbPath
	if p == "" {
		p = setup.ADBPath()
	}
	return adbkit.Client{ADB: perf.ADB{Path: p}}
}

func deviceContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func pickSerial(ctx context.Context, adbPath *string, requested string) (string, error) {
	if strings.TrimSpace(requested) != "" {
		return strings.TrimSpace(requested), nil
	}
	p := *adbPath
	if p == "" {
		p = setup.ADBPath()
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	devices, err := (perf.ADB{Path: p}).Devices(ctx)
	if err != nil {
		return "", fmt.Errorf("adb 不可用: %w", err)
	}
	for _, d := range devices {
		if d.State == "device" {
			return d.Serial, nil
		}
	}
	return "", fmt.Errorf("没有在线 Android 设备")
}

func writeJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func deviceInfo(args []string) int {
	fs, adbPath, serial := deviceFlags("device info")
	asJSON := fs.Bool("json", false, "输出 JSON")
	fs.Parse(args)
	ctx, cancel := deviceContext()
	defer cancel()
	s, err := pickSerial(ctx, adbPath, *serial)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	info, err := deviceClient(adbPath).Info(ctx, s)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON {
		writeJSON(info)
		return 0
	}
	fmt.Printf("serial: %s\n", info.Serial)
	fmt.Printf("model: %s  brand: %s\n", info.Model, info.Brand)
	fmt.Printf("android: %s  sdk: %s  abi: %s\n", info.Android, info.SDK, info.ABI)
	fmt.Printf("resolution: %s  density: %s\n", info.Resolution, info.Density)
	fmt.Printf("battery: %d%%  %.1f°C\n", info.BatteryLevel, info.BatteryTempC)
	return 0
}

func deviceInstall(args []string) int {
	fs, adbPath, serial := deviceFlags("device install")
	apk := fs.String("apk", "", "APK 路径")
	replace := fs.Bool("replace", true, "覆盖安装 -r")
	grant := fs.Bool("grant", true, "安装时授予运行时权限 -g")
	fs.Parse(args)
	if *apk == "" {
		fmt.Fprintln(os.Stderr, "缺少 -apk")
		return 1
	}
	ctx, cancel := deviceContext()
	defer cancel()
	s, err := pickSerial(ctx, adbPath, *serial)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	out, err := deviceClient(adbPath).Install(ctx, s, *apk, *replace, *grant)
	fmt.Println(out)
	if err != nil || !strings.Contains(strings.ToLower(out), "success") {
		if err != nil {
			fmt.Fprintln(os.Stderr, "安装失败:", err)
		}
		return 1
	}
	return 0
}

func deviceUninstall(args []string) int {
	fs, adbPath, serial := deviceFlags("device uninstall")
	pkg := fs.String("package", "", "包名")
	keep := fs.Bool("keep", false, "保留数据 -k")
	fs.Parse(args)
	if *pkg == "" {
		fmt.Fprintln(os.Stderr, "缺少 -package")
		return 1
	}
	ctx, cancel := deviceContext()
	defer cancel()
	s, err := pickSerial(ctx, adbPath, *serial)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	out, err := deviceClient(adbPath).Uninstall(ctx, s, *pkg, *keep)
	fmt.Println(out)
	if err != nil || !strings.Contains(strings.ToLower(out), "success") {
		if err != nil {
			fmt.Fprintln(os.Stderr, "卸载失败:", err)
		}
		return 1
	}
	return 0
}

func deviceStart(args []string) int {
	fs, adbPath, serial := deviceFlags("device start")
	pkg := fs.String("package", "", "包名")
	activity := fs.String("activity", "", "显式 activity；留空自动解析")
	cold := fs.Bool("cold", true, "冷启动（先 force-stop）")
	asJSON := fs.Bool("json", false, "输出 JSON")
	fs.Parse(args)
	if *pkg == "" {
		fmt.Fprintln(os.Stderr, "缺少 -package")
		return 1
	}
	ctx, cancel := deviceContext()
	defer cancel()
	s, err := pickSerial(ctx, adbPath, *serial)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dev := automation.NewADBDevice(perf.ADB{Path: *adbPath}, s)
	if dev.ADB.Path == "" {
		dev.ADB.Path = setup.ADBPath()
	}
	res, err := automation.Launch(ctx, dev, *pkg, *activity, *cold)
	if err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		return 1
	}
	if *asJSON {
		writeJSON(res)
		return 0
	}
	fmt.Printf("%s %s cold=%v measured=%v startup=%.0fms\n", s, res.Activity, res.Cold, res.Measured, res.StartupMS)
	return 0
}

func deviceStop(args []string) int {
	return simplePackageAction("device stop", args, func(ctx context.Context, c adbkit.Client, s, pkg string) (string, error) {
		return c.Stop(ctx, s, pkg)
	})
}

func deviceClear(args []string) int {
	fs, adbPath, serial := deviceFlags("device clear")
	pkg := fs.String("package", "", "包名")
	fs.Parse(args)
	if *pkg == "" {
		fmt.Fprintln(os.Stderr, "缺少 -package")
		return 1
	}
	ctx, cancel := deviceContext()
	defer cancel()
	s, err := pickSerial(ctx, adbPath, *serial)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	out, err := deviceClient(adbPath).Clear(ctx, s, *pkg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(strings.TrimSpace(out))
	if !strings.Contains(strings.ToLower(out), "success") {
		fmt.Fprintln(os.Stderr, "pm clear 未返回 Success")
		return 1
	}
	return 0
}

func simplePackageAction(name string, args []string, action func(context.Context, adbkit.Client, string, string) (string, error)) int {
	fs, adbPath, serial := deviceFlags(name)
	pkg := fs.String("package", "", "包名")
	fs.Parse(args)
	if *pkg == "" {
		fmt.Fprintln(os.Stderr, "缺少 -package")
		return 1
	}
	ctx, cancel := deviceContext()
	defer cancel()
	s, err := pickSerial(ctx, adbPath, *serial)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	out, err := action(ctx, deviceClient(adbPath), s, *pkg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(strings.TrimSpace(out))
	return 0
}

func devicePerm(verb string, args []string) int {
	fs, adbPath, serial := deviceFlags("device " + verb)
	pkg := fs.String("package", "", "包名")
	perms := fs.String("permissions", "", "逗号分隔权限")
	fs.Parse(args)
	if *pkg == "" || *perms == "" {
		fmt.Fprintln(os.Stderr, "需要 -package 和 -permissions")
		return 1
	}
	ctx, cancel := deviceContext()
	defer cancel()
	s, err := pickSerial(ctx, adbPath, *serial)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	list := splitCSV(*perms)
	client := deviceClient(adbPath)
	var out []string
	if verb == "grant" {
		out, err = client.Grant(ctx, s, *pkg, list)
	} else {
		out, err = client.Revoke(ctx, s, *pkg, list)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("%s %d 个权限\n", verb, len(out))
	return 0
}

func deviceScreenshot(args []string) int {
	fs, adbPath, serial := deviceFlags("device screenshot")
	out := fs.String("out", "", "输出 PNG；留空写到 QAWORK_HOME/screenshots")
	fs.Parse(args)
	ctx, cancel := deviceContext()
	defer cancel()
	s, err := pickSerial(ctx, adbPath, *serial)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	png, err := deviceClient(adbPath).Screenshot(ctx, s)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *out == "" {
		*out = filepath.Join(setup.HomeDir(), "screenshots", time.Now().Format("20060102-150405.000")+".png")
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(*out, png, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(*out)
	return 0
}

func deviceLogcat(args []string) int {
	fs, adbPath, serial := deviceFlags("device logcat")
	out := fs.String("out", "", "输出文件；留空打印到 stdout")
	clearFirst := fs.Bool("clear", false, "先清 logcat")
	lines := fs.Int("lines", 2000, "读取最后 N 行")
	fs.Parse(args)
	ctx, cancel := deviceContext()
	defer cancel()
	s, err := pickSerial(ctx, adbPath, *serial)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	logText, err := deviceClient(adbPath).LogcatDump(ctx, s, *lines, *clearFirst)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *out == "" {
		fmt.Print(logText)
		return 0
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(*out, []byte(logText), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(*out)
	return 0
}

func deviceMonkey(args []string) int {
	fs, adbPath, serial := deviceFlags("device monkey")
	pkg := fs.String("package", "", "包名")
	events := fs.Int("events", 1000, "事件数")
	throttle := fs.Int("throttle", 300, "事件间隔 ms")
	fs.Parse(args)
	if *pkg == "" {
		fmt.Fprintln(os.Stderr, "缺少 -package")
		return 1
	}
	ctx, cancel := deviceContext()
	defer cancel()
	s, err := pickSerial(ctx, adbPath, *serial)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	out, err := deviceClient(adbPath).Monkey(ctx, s, *pkg, *events, *throttle)
	fmt.Print(out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "monkey 失败:", err)
		return 1
	}
	return 0
}

func deviceAnimations(args []string) int {
	fs, adbPath, serial := deviceFlags("device animations")
	state := fs.String("state", "", "on 或 off")
	fs.Parse(args)
	if *state != "on" && *state != "off" {
		fmt.Fprintln(os.Stderr, "-state 需要 on 或 off")
		return 1
	}
	ctx, cancel := deviceContext()
	defer cancel()
	s, err := pickSerial(ctx, adbPath, *serial)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := deviceClient(adbPath).Animations(ctx, s, *state == "on"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("animations=%s\n", *state)
	return 0
}

func deviceWake(args []string) int {
	fs, adbPath, serial := deviceFlags("device wake")
	fs.Parse(args)
	ctx, cancel := deviceContext()
	defer cancel()
	s, err := pickSerial(ctx, adbPath, *serial)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := deviceClient(adbPath).Wake(ctx, s); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("awake")
	return 0
}

func deviceUnlock(args []string) int {
	fs, adbPath, serial := deviceFlags("device unlock")
	fs.Parse(args)
	ctx, cancel := deviceContext()
	defer cancel()
	s, err := pickSerial(ctx, adbPath, *serial)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := deviceClient(adbPath).Unlock(ctx, s); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("unlock sent")
	return 0
}

func devicePush(args []string) int {
	fs, adbPath, serial := deviceFlags("device push")
	local := fs.String("local", "", "本地文件")
	remote := fs.String("remote", "", "设备路径")
	fs.Parse(args)
	return deviceTransfer(adbPath, serial, "push", *local, *remote)
}

func devicePull(args []string) int {
	fs, adbPath, serial := deviceFlags("device pull")
	remote := fs.String("remote", "", "设备路径")
	local := fs.String("local", "", "本地文件")
	fs.Parse(args)
	return deviceTransfer(adbPath, serial, "pull", *remote, *local)
}

func deviceTransfer(adbPath, serial *string, verb, a, b string) int {
	if a == "" || b == "" {
		fmt.Fprintln(os.Stderr, "需要 -local 和 -remote")
		return 1
	}
	ctx, cancel := deviceContext()
	defer cancel()
	s, err := pickSerial(ctx, adbPath, *serial)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	c := deviceClient(adbPath)
	var out string
	if verb == "push" {
		out, err = c.Push(ctx, s, a, b)
	} else {
		out, err = c.Pull(ctx, s, a, b)
	}
	fmt.Print(out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
