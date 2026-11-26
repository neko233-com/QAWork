# AGENTS.md

QAWork 的工程规则。改代码前先读这一页。

## 栈与硬约束

- **Go 1.27+，纯 Go，`CGO_ENABLED=0`**。不允许 `import "C"`，不允许 C/C++ 构建依赖。
- **禁止 GitHub Actions**：仓库不允许 `.github/workflows/`，不允许任何云端 CI、Action、自动构建/发布 workflow。验证走 `scripts/verify.ps1`，发布走 `scripts/release.ps1` + `gh release create`，两者都在本机执行。
- **构建硬约束**：所有构建、测试都显式 `CGO_ENABLED=0`；验证时 `go env CGO_ENABLED` 必须是 `0`。引入 cgo 的 PR 直接拒。
- **原生 UI**：`github.com/egoist/mygo`（`ui.View` / `ui.Context`）+ `github.com/lobehub/lobe-mygo/pkg/theme`（设计令牌）。不用 WebView、HTML、CSS、JS。
- **宿主**：只支持 `windows/amd64` 与 `darwin/arm64`，各出 `qawork` / `qawork-gui`。不上 Linux/32 位。当前机器是什么平台就只做该平台真实验证；另一平台只做 `CGO_ENABLED=0 GOOS=... GOARCH=... go build ./...` 交叉编译。
- **仓库是 public**：`https://github.com/neko233-com/QAWork`，默认分支 `main`。

## 工作模式（固定，不许漂移）

| # | 模式 | 状态 | 竞品/口径 |
| --- | --- | --- | --- |
| 0 | **PerfDog 性能**（默认） | 可用 | [腾讯 PerfDog](https://wetest.qq.com/products/perfdog)，只对齐采集口径 |
| 1 | 兼容性 | 可用 | 多设备矩阵、重复轮次、crash/ANR，对标 WeTest 云真机场景 |
| 2 | 自动化 | 可用 | `qawork.yaml` + adb 步骤/断言 + 性能门禁 |
| 3 | 压测 | 可用 | monkey 长随机输入 + 采样 + crash/ANR |

规则：

- `internal/app` 的 `Mode` 默认必须是 `0`（PerfDog 性能），GUI 启动即进入该模式。
- 新增/改名模式要同时改三处：`internal/app` 的模式常量与标签、README「工作模式」、本表。
- 未实现的模式只能显示"规划中"，不许在 UI 里给出假数据、假进度、假报告。

## 分层

| 包 | 职责 | 不许做 |
| --- | --- | --- |
| `internal/perf` | 采样器（Android adb / Windows 与 macOS 本机进程）、统计、CSV、门禁、logcat 分析 | 依赖 UI 包 |
| `internal/adbkit` | 显式 adb 能力层：设备信息、安装/启停/清数据/权限、截图、logcat、UI dump、monkey | 任意 shell 透传；依赖 UI 包 |
| `internal/automation` | `qawork.yaml` 解析/校验、adb 步骤、断言、单 case 执行、产物 | 依赖 UI 包；另写一份采样器 |
| `internal/compat` | 多设备矩阵、重复轮次、APK 安装、设备信息、crash/ANR、CSV/JSON/HTML/JUnit | 依赖 UI 包；伪造兼容结果 |
| `internal/stress` | monkey 压测编排、采样、crash/ANR、证据落盘 | 依赖 UI 包；伪造样本 |
| `internal/app` | 工作台状态与视图（四模式 + 环境页） | 直接 `exec adb`（走 `perf.ADB` / `adbkit`） |
| `cmd/qawork` | 无头 CLI | 复制一份采样/自动化/压测逻辑 |
| `cmd/qawork-gui` | 窗口入口 | 业务逻辑 |

GUI 和 CLI 必须共用同一份采样与 CSV 口径。自动化只允许经 `automation.Execute`，兼容只允许经 `compat.Run`，压测只允许经 `stress.Run`；GUI/CLI/MCP 不许各写一套。

## Agent 接口（CLI / MCP）

- CLI 的 `devices`、`device info/start`、`perf` / `report` / `setup` / `run` / `compat` / `stress` 支持 `-json`；动作类子命令输出纯文本。门禁/兼容/压测失败退出码 2，环境缺必装项退出码 2，执行错误退出码 1。
- `qawork device` 只暴露显式操作：`info / install / uninstall / start / stop / clear / grant / revoke / screenshot / logcat / monkey / animations / wake / unlock / push / pull`。**不提供任意 shell**，执行 `shell` 子命令直接拒。
- `qawork.yaml` 步骤：`launch`（仅第一步）/ `wait` / `tap` / `longpress` / `doubletap` / `swipe` / `drag` / `keyevent` / `text`（仅 ASCII）/ `stop` / `clear` / `grant` / `revoke` / `wait_activity` / `wait_text` / `assert_activity` / `assert_text` / `assert_not_text` / `assert_package` / `screenshot`。
- `wait_activity` / `assert_activity` 用 `activity:` 或 `text:` 指定目标；`wait_text` / `assert_text` / `assert_not_text` 用 `text:`，依赖 uiautomator accessibility tree，SurfaceView 游戏可能读不到。阈值 nil=继承、显式 0=关闭。
- `internal/mcp` 是唯一 MCP 实现，stdio JSON-RPC 2.0；**stdout 只准写协议帧**，日志/进度一律 stderr。
- MCP 暴露 `qawork_doctor` / `qawork_devices` / `qawork_device_info` / `qawork_logcat` / `qawork_app` / `qawork_perf` / `qawork_report` / `qawork_validate_plan` / `qawork_run_plan` / `qawork_compat_matrix` / `qawork_stress`，共 11 个。不做任意 shell/adb 透传。
- MCP 工具与 CLI 共用 `perf`、`automation.Execute`、`compat.Run`、`stress.Run`；改参数/字段要同步 `docs/agent.md`。
- Agent 验证顺序：**Android 真机/模拟器优先**；PC 进程只验证 CPU/内存，不能拿它当帧率证据。
- 新增/修改工具参数必须补 `internal/mcp` 单测，协议测试不许起真实 adb。

## 环境安装工具

- `internal/setup` 是唯一实现，GUI 环境页和 `qawork setup` 共用；不许在 UI 层复制下载/解压逻辑。
- **adb 必装**，scrcpy 可选，Android USB 驱动标记 `manual`；驱动不许给假安装按钮。
- 安装只写当前用户目录（Windows `%APPDATA%\QAWork\tools`、macOS `~/Library/Application Support/QAWork/tools`，`QAWORK_HOME` 可覆盖）；Windows 写 HKCU 用户 PATH，macOS 写 `~/.zprofile` 的受管块。**不需要管理员**，不写系统目录/系统 PATH。
- macOS 免厂商 USB 驱动；Windows 的 Android USB 驱动仍标 `manual`。
- 测试不得修改真实注册表或真实用户 PATH；解压必须保留 `..` 越界防护；macOS 解压后要补 `chmod +x`。

## 测试

- 提交前 `scripts/verify.ps1` 必须全绿；它包含 `CGO_ENABLED=0`、`gofmt -l .`、`go vet ./...`、`go test -count=1 ./...`、Windows 构建、macOS arm64 交叉编译。
- **没有 CI 兜底**：本地脚本就是唯一门禁。PR 描述里要写实际跑过的命令；没跑过的平台标注"未真机验证"。
- 解析器、CSV、门禁、MCP 协议、setup 解压/PATH 拼接属纯逻辑，必须有单测；`automation` 的计划解析/校验/Launch 解析/StepCommand/runner（fake device + fake sampler）、`compat` 的 WM size/logcat/CSV/报告、`stress` 的包名校验/截断也必须有单测。
- 真机采样、SurfaceFlinger 层、Unity/UE 线程、真实多设备矩阵属硬件路径，单测不覆盖时必须在文档标"需真机验收"，不许假装测过。
- 覆盖率用 `go test -cover ./...` 记录，不追求数字；硬件路径低覆盖要在 PR 里说明原因。

## 维护

- 日常维护者：**@akai183**（2026-11 起接手；写权限已开）。仓库 owner 仍是 [neko233-com](https://github.com/neko233-com)。
- 接手范围：日常 PR/issue、引擎与 Android 口径、本地验证绿线；发版 tag 与仓库设置由 owner 把关。

## 数据诚实原则

- 没接的数据不许编。兼容矩阵只写在设备上真实跑出来的行；失败、crash、ANR 必须带 logcat/截图证据。
- 采集不到帧率的目标（PC 进程）标记为"无帧率来源"，门禁跳过帧率检查，并在 UI/CLI 明确写出来。macOS 本机进程用 `ps` 取 CPU/RSS，没有 FPS。
- 新增指标必须在 `docs/perfdog-mode.md` 写清来源、单位、已知边界；自动化/兼容/压测口径写在 `docs/automation.md`。
- 引擎识别与线程口径以 `docs/engines.md` 为准：Unity 认 `UnityMain`/`UnityGfxDeviceW`，UE5 认 `GameThread`/`RenderThread`/`RHIThread`，未命中写 `native`，不许猜引擎。引擎名匹配必须保留"只对 ≥8 字符名字做前后缀容错"。

## 本机验证（当前 Windows）

```powershell
.\scripts\verify.ps1

# 真实设备冒烟（有设备时）
.\dist\verify\qawork.exe devices
.\dist\verify\qawork.exe device info -json
.\dist\verify\qawork.exe device start -package com.android.settings -json
.\dist\verify\qawork.exe device screenshot -out .\dev.png
.\dist\verify\qawork.exe run -plan examples\qawork.yaml -validate
.\dist\verify\qawork.exe run -plan examples\qawork.yaml -serial <serial> -artifacts automation-smoke
.\dist\verify\qawork.exe compat -package <pkg> -devices <serial> -repeat 2 -artifacts compat-smoke
.\dist\verify\qawork.exe stress -package <pkg> -serial <serial> -duration 30s -artifacts stress-smoke
.\dist\verify\qawork-gui.exe -demo -capture .\docs\shot.png   # 首帧自检
.\dist\verify\qawork-gui.exe -setup -capture .\docs\setup.png # 环境页自检
```

macOS arm64 在当前 Windows 上只交叉编译，不假装真机跑过：

```powershell
$env:CGO_ENABLED="0"; $env:GOOS="darwin"; $env:GOARCH="arm64"
go build ./...
```

macOS 本机验证（有 Mac 时）：

```bash
CGO_ENABLED=0 go test -count=1 ./...
CGO_ENABLED=0 go build ./cmd/qawork ./cmd/qawork-gui
```

MCP 协议冒烟（不需要设备）：

```powershell
'{"jsonrpc":"2.0","id":1,"method":"initialize"}' | .\dist\verify\qawork.exe mcp
```

改完 UI 必须重新生成 `docs/shot.png`，README 用它当门面；环境页改动同时重生成 `docs/setup.png`。

## 发布（禁止 Actions）

1. 改 `internal/version/version.go` 与 `mygo.json` 的版本号。
2. `.\scripts\verify.ps1` 全绿。
3. `.\scripts\release.ps1`：本机构建 Windows amd64 + macOS arm64，打 zip，`gh release create vX.Y.Z` 上传。
4. 不许新增 `.github/workflows/`，不许把构建/发布搬回 GitHub。

## 提交信息

一句话说清"改了什么、为什么"，别写"update"。中文或英文都行，保持一致。
