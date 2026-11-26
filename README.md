# QAWork

**把 QA 流程搬回本地**：Go 1.27 + MyGo（GPUI 思路的原生 GPU 桌面 UI）写的游戏 QA 工作台，无 WebView、无浏览器、纯 Go 零 CGO。
默认工作模式就是 **PerfDog 性能模式**：选设备、填包名、按固定间隔采帧率/CPU/内存/温度，跑完按阈值盖门禁。

![QAWork 工作台](docs/shot.png)

## 三种工作模式

| 模式 | 状态 | 干什么 |
| --- | --- | --- |
| **PerfDog 性能**（默认） | 可用 | adb 采集帧率、帧耗时 P50/P95/P99、卡顿帧、CPU、内存、温度；写 CSV，按阈值判定 |
| 兼容性 | 规划中 | 本地设备农场跑多机型安装/冷启/崩溃 ANR 矩阵，不上传安装包 |
| 自动化 | 规划中 | `qawork.yaml` 用例 + adb 回放，跑完自动盖性能门禁 |

## 为什么不是 WeTest 的复制品

WeTest 是腾讯的质量开放平台，强在云真机池和 SaaS 协作；QAWork 走的是另一条路：

| 维度 | WeTest（云平台） | QAWork（本地工作台） |
| --- | --- | --- |
| 形态 | 网页 + 云真机 + 客户端 SDK | 原生桌面窗口 + CLI，离线可用 |
| 数据 | 上传云端 | CSV/JSON 落本地，直接进 CI |
| 性能采集 | 云真机 / PerfDog 系列工具 | 内置 PerfDog 口径：SurfaceFlinger 帧延迟 + `/proc` 进程指标 |
| 兼容性 | 租云真机跑矩阵 | 接自己的设备池，零上传 |
| 自动化 | 云端用例编排 | 本地用例 + CI 触发 |
| 成本 | 按设备时长计费，以官方为准 | 开源免费，硬件自己的 |

定位很清楚：**WeTest 解决"没设备、要协作"，QAWork 解决"有设备、要快、要能进流水线"。**

## 快速开始

需要 Go 1.27+，`CGO_ENABLED=0` 可直接构建。

```powershell
go build -o qawork-gui.exe .\cmd\qawork-gui   # 桌面工作台
go build -o qawork.exe .\cmd\qawork           # CLI
.\qawork-gui.exe
```

自检渲染（不开窗口给人看，截首帧）：

```powershell
.\qawork-gui.exe -capture .\docs\shot.png
.\qawork-gui.exe -demo -capture .\docs\shot.png   # 采本机进程 8 秒后再截图
```

### CLI

```powershell
qawork devices                                   # 列 adb 设备
qawork perf -serial 127.0.0.1:16384 -package com.example.game -duration 60s -csv run.csv
qawork perf -serial local -package Game.exe -duration 30s          # 本机进程（无帧率）
qawork report -csv run.csv -min-fps 50 -max-jank 5                 # 只汇总和判定
```

`perf` 和 `report` 在门禁不通过时退出码 2，可直接当 CI 卡点。`-json` 输出机器可读结果。

## 采集口径

| 指标 | Android | 本机进程（Windows） |
| --- | --- | --- |
| FPS | `dumpsys SurfaceFlinger --latency` 取应用层的实际上屏时间戳；取不到时退到 `gfxinfo` 帧数增量 | 无（需要 Present hook，规划中） |
| 帧耗时 | 1000/FPS，汇总时算 P50/P95/P99 | 无 |
| 卡顿帧 | `gfxinfo` 的 Janky frames 增量占比 | 无 |
| CPU | `/proc/<pid>/stat` 的 utime+stime 增量（100 Hz jiffies） | `GetProcessTimes` 内核+用户时间增量 |
| 内存 | `/proc/<pid>/stat` 的 rss 页数 × 4 KiB | PSAPI working set |
| 温度 | `dumpsys battery` 的电池温度 | 无 |

细节与已知边界见 [docs/perfdog-mode.md](docs/perfdog-mode.md)。

## 门禁

默认阈值：平均 FPS ≥ 48、卡顿帧 ≤ 5%、CPU ≤ 70%、内存 ≤ 1024 MB、温度 ≤ 45 °C。
没有帧率来源的目标（PC 进程）自动跳过帧率类检查，不会假装通过。

```powershell
qawork perf -serial $env:ADB_SERIAL -package com.example.game -duration 60s -csv run.csv -min-fps 55
if ($LASTEXITCODE -eq 2) { throw "性能门禁未通过" }
```

## 现在能做什么，不能做什么

能：真机/模拟器上采一个应用的真实帧率、CPU、内存、温度；本机进程采 CPU/内存；CSV 与 JSON；门禁退出码。
不能：PC 游戏帧率（要注入 Present/ETW，未做）；SoC 温度（只有电池温度）；兼容性与自动化（只有面板和路线图，没有假数据）。

完整路线见 [docs/roadmap.md](docs/roadmap.md)。

## 工程约定

- Go 1.27，`CGO_ENABLED=0`，不引入 `import "C"`。
- UI 只用 `github.com/egoist/mygo` + `github.com/lobehub/lobe-mygo`，不碰 WebView。
- 采集层与 UI 解耦：`internal/perf` 同时服务 GUI 和 CLI，一份 CSV 口径。
- 任何"还没接"的功能，面板里写清楚，不摆假数据。

## 商标

WeTest、PerfDog 是腾讯的商标/产品。QAWork 与腾讯无隶属关系，"PerfDog 模式"仅表示采集口径对齐，便于横向比较。

## License

MIT © 2026 neko233-com
