# PerfDog 性能模式：采集口径

QAWork 的默认模式。名字里的 PerfDog 指腾讯 WeTest 旗下的性能测试工具（竞品页：<https://wetest.qq.com/products/perfdog>），这里表示“指标口径对齐”，不代表任何隶属或兼容承诺。

## 一次采集的流程

1. 选中设备（`adb devices -l`；`local` 表示本机进程）。
2. 校验目标存在：`pidof <pkg>` 或按进程名找 PID。
3. `dumpsys gfxinfo <pkg> reset`：把帧计数清零，避免第一格是应用启动至今的累计值。
4. 每 `interval` 采一格，落 `Sample`，同时追加 CSV。
5. 结束时算汇总、跑阈值门禁。

## 指标定义

| 字段 | 定义 | 来源 |
| --- | --- | --- |
| `fps` | 该间隔的实际上屏帧率 | SurfaceFlinger `--latency`：**比上次读取更新的** `actualPresentTime` 条数 / 采样间隔 |
| `frame_ms` | `1000/fps`，间隔内平均帧耗时 | 计算值 |
| `jank_pct` | 该间隔内 Janky frames 占渲染帧的比例 | `dumpsys gfxinfo` 计数增量 |
| `cpu_pct` | 单核口径 CPU 占用（可超过 100，按 `nproc` 截顶） | `/proc/<pid>/stat` utime+stime 增量，100 Hz |
| `mem_mb` | 进程 RSS | `/proc/<pid>/stat` rss 页 × 4 KiB |
| `temp_c` | **电池**温度 | `dumpsys battery` 的 temperature（0.1 °C），读到 0 时沿用上次有效值 |
| `threads` | 线程数 | `/proc/<pid>/stat` num_threads |
| `engine` | 引擎类型：`unity` / `ue` / `native` | 进程线程名识别，见 [engines.md](engines.md) |
| `main_thread` / `main_cpu` | 引擎主线程名与单核 CPU 占比 | `/proc/<pid>/task/<tid>/stat` 增量 |
| `render_thread` / `render_cpu` | 引擎渲染线程名与单核 CPU 占比 | 同上 |

汇总（`perf.Summarize`）：平均/最低 FPS、帧耗时 P50/P95/P99、平均卡顿率、CPU 峰值与均值、内存峰值、温度峰值。
`FrameSamples` 记录“有帧率数据的格数”，为 0 时门禁自动跳过帧率检查。

## 选层规则

`PickLayer` 在 `dumpsys SurfaceFlinger --list` 里按顺序：优先 `SurfaceView[<pkg>`（游戏自绘层），否则取第一个含包名且不是 `Background for` / `ActivityRecord` 的层。

## 已知边界（不许在文档里美化）

- **静态界面会掉到 0 fps**：帧率只数本次间隔内新出现的上屏时间戳，屏幕不动就是 0，不会把上一轮的帧率当成现在。
- **SurfaceFlinger 是 127 帧的环形缓冲**：1 秒间隔最多数到 127 帧，所以 `fps` 上限按 240 截顶；测 120/144 fps 建议 `-interval 500ms`，否则会被缓冲长度压低。
- **温度不是 SoC**：只有电池温度，热节流要看 `thermalservice`，未接入。
- **CPU 是单核口径**：要看整机占用得再乘/除核数，报告里保持单核便于横向比较。
- **PC 进程没有帧率**：Windows/macOS 上不注入 Present hook 就拿不到帧率，硬编一个“60 fps”就是造假；当前明确标记“无帧率来源”。
- **jiffies 假设 100 Hz**：Android 常见 100 Hz，个别内核不同；未做探测，写在这里免得出错没人知道。
- **引擎靠线程名**：识别依据是 Unity/UE 自身的线程命名，改线程名的自定义引擎会落到 `native`；UE5.8 不解析 `stat`、不探测版本，细节见 [engines.md](engines.md)。

## CSV 列

`at,fps,frame_ms,jank_pct,cpu_pct,mem_mb,temp_c,threads,engine,main_thread,main_cpu,render_thread,render_cpu,note`

旧版 9 列（`...,threads,note`）的 CSV 仍能读：按表头是否含 `engine` 判断，老文件按 `native` 处理。

`note` 记录数据来源（`SurfaceFlinger` / `gfxinfo` / `无新帧` / `PC 进程（无帧率）`），排查异常先看这一列。
