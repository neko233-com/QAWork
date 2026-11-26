# QAWork

**把 QA 流程搬回本地**：Go 1.27 + MyGo（GPUI 思路的原生 GPU 桌面 UI）写的游戏 QA 工作台，无 WebView、无浏览器、纯 Go 零 CGO。
默认工作模式就是 **PerfDog 性能模式**：选设备、填包名、按固定间隔采帧率/CPU/内存/温度，跑完按阈值盖门禁。

![QAWork 工作台](docs/shot.png)

## 为什么不是 PerfDog 的复制品

竞品很明确：[腾讯 PerfDog](https://wetest.qq.com/products/perfdog)——成熟、跨端、开箱即用的性能测试工具。QAWork 不和它拼功能数量，拼的是"能不能留在自己的流水线里"：

| 维度 | PerfDog（腾讯 WeTest 产品） | QAWork（本地工作台） |
| --- | --- | --- |
| 形态 | 桌面客户端 + 账号体系，云同步报告 | 开源原生窗口 + CLI，离线可用 |
| 平台 | Android / iOS 等多端，覆盖成熟 | Android + Windows 本机进程（CPU/内存），iOS 未接 |
| 数据 | 平台报告为主 | CSV/JSON 落本地，直接进 CI |
| 自动化 | 客户端内报告与分析 | 退出码 2 的门禁，任意 CI 直接卡 |
| 二开 | 闭源 | 纯 Go，改采样器、接自有设备池 |
| 成本 | 免费额度 + 订阅，以官方为准 | 开源免费，硬件自己的 |

定位：**PerfDog 解决"拿来就能测"，QAWork 解决"测完能卡住发布"。** 短期不追它的平台覆盖，先把 Android 口径和 CI 门禁做扎实。

## 工作模式

当前声明三种，只有第一种是可用状态，另外两种是明确标注的规划，不摆假数据：

| # | 模式 | 状态 | 说明 |
| --- | --- | --- | --- |
| 0 | **PerfDog 性能**（默认） | 可用 | 对标 [PerfDog](https://wetest.qq.com/products/perfdog) 的采集口径 |
| 1 | 兼容性 | 规划中 | 本地设备农场跑安装/冷启/崩溃矩阵，不属于 PerfDog 的能力范围 |
| 2 | 自动化 | 规划中 | `qawork.yaml` 用例 + adb 回放 + 性能门禁 |

模式编号与默认值写死在 `internal/app`（`Mode` 默认 0），文档、UI、CLI 三处必须一致。

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
qawork setup check                               # 检查 adb / scrcpy / 驱动
qawork setup install -id adb                     # 必装；已装则跳过
qawork perf -serial 127.0.0.1:16384 -package com.example.game -duration 60s -csv run.csv
qawork perf -serial local -package Game.exe -duration 30s          # 本机进程（无帧率）
qawork report -csv run.csv -min-fps 50 -max-jank 5                 # 只汇总和判定
qawork mcp                                                         # stdio MCP，给 Agent
```

`perf` 和 `report` 在门禁不通过时退出码 2，可直接当 CI 卡点。`-json` 输出机器可读结果。

## Agent 原生：CLI / MCP

QAWork 从设计上就是给 Agent 用的，不只是给人点的窗口：

- **CLI**：`devices` / `perf` / `report` / `setup` 全部支持 `-json`，门禁失败退出码 2，Agent 可以直接判断。
- **MCP**：`qawork.exe mcp` 起 stdio JSON-RPC，暴露 `qawork_doctor`、`qawork_devices`、`qawork_perf`、`qawork_report` 四个工具。采样器、CSV 与 GUI 完全同源。

```json
{
  "mcpServers": {
    "qawork": { "command": "D:\\Tools\\qawork.exe", "args": ["mcp"] }
  }
}
```

典型 Agent 流程：`qawork_doctor` 修环境 → `qawork_devices` 拿序列号 → `qawork_perf` 采 10–30s → 看 `summary.engine` 的主/渲线程 → `qawork_report` 换阈值复判。**Android 是首选验证平台**；PC 进程只采 CPU/内存，没有帧率来源。

完整协议、参数、JSON 字段和边界见 [docs/agent.md](docs/agent.md)。

## 环境安装（独立 UI）

主界面右上角 **环境**，或 `qawork-gui.exe -setup`，打开独立的环境安装界面：每个依赖单独一行、单独安装，不搞全家桶。

| 依赖 | 级别 | 说明 |
| --- | --- | --- |
| **adb（Platform Tools）** | **必装** | 没有它 Android 采样完全不可用；从 Google 官方下载，装进 `%APPDATA%\QAWork\tools`，写当前用户 PATH |
| scrcpy | 可选 | 投屏/录屏/点按调试，不影响采样口径 |
| Android USB 驱动 | 手动 | 厂商驱动，QAWork 不代装、不改驱动配置 |

无需管理员权限，不写系统目录。CLI 同样可用：`qawork setup check -json`、`qawork setup install -id adb`。细节、卸载和边界见 [docs/setup.md](docs/setup.md)。

![环境安装](docs/setup.png)

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

## 引擎支持（Unity / UE5）

不注入引擎、不改包、不需要可调试 APK，靠进程自己的线程名识别：

| 引擎 | 识别依据 | 主线程 | 渲染线程 |
| --- | --- | --- | --- |
| Unity | `UnityMain` / `UnityGfxDeviceW` | `UnityMain` | `UnityGfxDeviceW` |
| UE5（含 5.8） | `GameThread`（配 `RenderThread`/`RHIThread`） | `GameThread` | `RenderThread`，无则 `RHIThread` |
| 原生 | 以上都不命中 | — | — |

识别到引擎后，CSV 多出 `engine/main_thread/main_cpu/render_thread/render_cpu` 五列，CLI 逐样本和汇总都会打印主/渲线程的单核 CPU 占比；UI 侧栏显示引擎 badge，表格有"主/渲"列。没识别到就留空，显示 `—`，不猜。

Android 的线程名会被截断（`comm` 只有 15 字符，某些 ROM 留尾），匹配做了前后缀容错，并只对 8 字符以上的名字容错，避免把 `main` 误认成 `UnityMain`。

**UE5.8 只到线程与帧层口径**：不解析 `stat unit`、不探测引擎版本、不接 Unity Profiler。细节和验收现状见 [docs/engines.md](docs/engines.md)。

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

WeTest、PerfDog 是腾讯的商标/产品，竞品页为 <https://wetest.qq.com/products/perfdog>。QAWork 与腾讯无隶属关系，"PerfDog 模式"仅表示采集口径对齐，便于横向比较。

## License

MIT © 2026 neko233-com
