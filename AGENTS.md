# AGENTS.md

QAWork 的工程规则。改代码前先读这一页。

## 栈与约束

- **Go 1.27+，纯 Go，`CGO_ENABLED=0`**。不允许 `import "C"`，不允许 C/C++ 构建依赖。
- **构建硬约束**：所有构建、测试、CI 都显式 `CGO_ENABLED=0`；验证时 `go env CGO_ENABLED` 必须是 `0`。引入 cgo 的 PR 直接拒，不讨论"临时开一下"。
- **原生 UI**：`github.com/egoist/mygo`（`ui.View` / `ui.Context`）+ `github.com/lobehub/lobe-mygo/pkg/theme`（设计令牌）。不用 WebView、HTML、CSS、JS。
- **发布目标**：`windows/amd64` 的 `qawork.exe` 与 `qawork-gui.exe`，产物进 `dist/`。macOS 不是当前验收目标。

## 工作模式（固定，不许漂移）

| # | 模式 | 状态 | 竞品/口径 |
| --- | --- | --- | --- |
| 0 | **PerfDog 性能**（默认） | 可用 | 竞品：[腾讯 PerfDog](https://wetest.qq.com/products/perfdog)，只对齐采集口径 |
| 1 | 兼容性 | 规划中 | 本地设备农场矩阵，对标 WeTest 云真机的场景，不属于 PerfDog 能力 |
| 2 | 自动化 | 规划中 | `qawork.yaml` + adb 回放 + 性能门禁 |

规则：

- `internal/app` 的 `Mode` 默认必须是 `0`（PerfDog 性能），GUI 启动即进入该模式。
- 新增/改名模式要同时改三处：`internal/app` 的模式常量与标签、README「工作模式」、本表。
- 未实现的模式只能显示"规划中"，不许在 UI 里给出假数据、假进度、假报告。
- 竞品固定指 PerfDog 产品页 <https://wetest.qq.com/products/perfdog>；WeTest 只作为其母平台在必要处提及，不要混为同一竞品。

## 分层

| 包 | 职责 | 不许做 |
| --- | --- | --- |
| `internal/perf` | 采样器（Android adb / 本地进程）、统计、CSV、门禁 | 依赖 UI 包 |
| `internal/app` | 工作台状态与视图 | 直接 `exec adb`（走 `perf.ADB`） |
| `cmd/qawork` | 无头 CLI，CI 用 | 复制一份采样逻辑 |
| `cmd/qawork-gui` | 窗口入口 | 业务逻辑 |

GUI 和 CLI 必须共用同一份采样与 CSV 口径，否则"窗口里看到的"和"CI 判的"会打架。

## Agent 接口（CLI / MCP）

- CLI 的 `devices` / `perf` / `report` / `setup` 都必须支持 `-json`，门禁失败退出码 2，环境缺必装项退出码 2。
- `internal/mcp` 是唯一的 MCP 实现，stdio JSON-RPC 2.0；**stdout 只准写协议帧**，日志/进度一律 stderr。
- MCP 只暴露 `qawork_doctor` / `qawork_devices` / `qawork_perf` / `qawork_report`，不提供任意 shell/adb 透传。
- MCP 工具与 CLI 共用 `perf` 采样器，不许另写一份；改参数/字段要同步 `docs/agent.md`。
- Agent 的验证顺序：**Android 真机/模拟器优先**；PC 进程只验证 CPU/内存，不能拿它当帧率证据。
- 新增/修改工具参数必须补 `internal/mcp` 单测，协议测试不许起真实 adb。

## 环境安装工具

- `internal/setup` 是唯一实现，GUI 环境页和 `qawork setup` 共用；不许在 UI 层复制下载/解压逻辑。
- **adb 必装**，scrcpy 可选，Android USB 驱动标记 `manual`；驱动不许给假安装按钮。
- 安装只写当前用户目录（`%APPDATA%\QAWork\tools`，`QAWORK_HOME` 可覆盖）和 HKCU 用户 PATH，**不需要管理员**，不写系统目录/系统 PATH。
- 测试不得修改真实注册表或真实用户 PATH；解压必须保留 `..` 越界防护。
- 环境依赖变化要同步 `docs/setup.md`，并把"必装"原因写清楚。

## 测试

- 提交前 `go test -count=1 ./...` 必须全绿；CI 同命令。
- 解析器、CSV、门禁、MCP 协议、setup 解压/PATH 拼接属纯逻辑，**必须有单测**。
- 真机采样、SurfaceFlinger 层、Unity/UE 线程属硬件路径，单测不覆盖时必须在文档标"需真机验收"，不许假装测过。
- 覆盖率用 `go test -cover ./...` 记录，不追求数字；硬件路径低覆盖要在 PR 里说明原因。

## 维护

- 日常维护者：**@akai183**（2026-11 起接手；写权限已开）。仓库 owner 仍是 [neko233-com](https://github.com/neko233-com)。
- 接手范围：日常 PR/issue、引擎与 Android 口径、CI 绿线；发版 tag 与仓库设置由 owner 把关。

## 数据诚实原则

- 没接的数据不许编。兼容性、自动化面板写"规划中"，不摆假报表、假设备。
- 采集不到帧率的目标（PC 进程）标记为"无帧率来源"，门禁跳过帧率检查，并在 UI/CLI 明确写出来。
- 新增指标必须在 `docs/perfdog-mode.md` 写清来源、单位、已知边界。
- 引擎识别与线程口径以 `docs/engines.md` 为准：Unity 认 `UnityMain`/`UnityGfxDeviceW`，UE5 认 `GameThread`/`RenderThread`/`RHIThread`，未命中写 `native`，不许猜引擎。引擎名匹配必须保留"只对 ≥8 字符名字做前后缀容错"，否则 `main` 会误配 `UnityMain`。

## 在 Windows 上必须做的验证

```powershell
$env:CGO_ENABLED = "0"
gofmt -w .
go vet ./...
go test -count=1 ./...
go test -cover ./...
go build -o qawork.exe .\cmd\qawork
go build -o qawork-gui.exe .\cmd\qawork-gui
.\qawork.exe version
.\qawork.exe setup check -json
.\qawork-gui.exe -demo -capture .\docs\shot.png   # 首帧自检，人工看一眼
.\qawork-gui.exe -setup -capture .\docs\setup.png # 环境页自检
```

MCP 协议冒烟（不需要设备）：

```powershell
'{"jsonrpc":"2.0","id":1,"method":"initialize"}' | .\qawork.exe mcp
```

有设备时补一条真实采集（只读操作）：

```powershell
.\qawork.exe perf -serial <serial> -package <pkg> -duration 8s -csv smoke.csv
```

改完 UI 必须重新生成 `docs/shot.png`，README 用它当门面；环境页改动同时重生成 `docs/setup.png`。

## 发布

打 tag `vX.Y.Z` 触发 `.github/workflows/ci.yml`，上传 Windows 产物并建 Release。
版本号同时改 `internal/version/version.go` 和 `mygo.json`。

## 提交信息

一句话说清"改了什么、为什么"，别写"update"。中文或英文都行，保持一致。
