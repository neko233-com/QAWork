# AGENTS.md

QAWork 的工程规则。改代码前先读这一页。

## 栈与约束

- **Go 1.27+，纯 Go，`CGO_ENABLED=0`**。不允许 `import "C"`，不允许 C/C++ 构建依赖。
- **原生 UI**：`github.com/egoist/mygo`（`ui.View` / `ui.Context`）+ `github.com/lobehub/lobe-mygo/pkg/theme`（设计令牌）。不用 WebView、HTML、CSS、JS。
- **发布目标**：`windows/amd64` 的 `qawork.exe` 与 `qawork-gui.exe`，产物进 `dist/`。macOS 不是当前验收目标。
- **产品定位**：产品名 QAWork，对标腾讯 WeTest；**默认工作模式 = PerfDog 性能模式**。这条不许在迭代中漂移。

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
