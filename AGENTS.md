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

## 数据诚实原则

- 没接的数据不许编。兼容性、自动化面板写"规划中"，不摆假报表、假设备。
- 采集不到帧率的目标（PC 进程）标记为"无帧率来源"，门禁跳过帧率检查，并在 UI/CLI 明确写出来。
- 新增指标必须在 `docs/perfdog-mode.md` 写清来源、单位、已知边界。

## 在 Windows 上必须做的验证

```powershell
$env:CGO_ENABLED = "0"
gofmt -w .
go vet ./...
go test -count=1 ./...
go build -o qawork.exe .\cmd\qawork
go build -o qawork-gui.exe .\cmd\qawork-gui
.\qawork.exe version
.\qawork-gui.exe -demo -capture .\docs\shot.png   # 首帧自检，人工看一眼
```

有设备时补一条真实采集（只读操作）：

```powershell
.\qawork.exe perf -serial <serial> -package <pkg> -duration 8s -csv smoke.csv
```

改完 UI 必须重新生成 `docs/shot.png`，README 用它当门面。

## 发布

打 tag `vX.Y.Z` 触发 `.github/workflows/ci.yml`，上传 Windows 产物并建 Release。
版本号同时改 `internal/version/version.go` 和 `mygo.json`。

## 提交信息

一句话说清"改了什么、为什么"，别写"update"。中文或英文都行，保持一致。
