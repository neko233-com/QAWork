# Unity 与 UE5 支持口径

QAWork 对 Unity 和 Unreal Engine 5 的支持，落在**指标口径**上：能正确认出引擎、帧落在哪一层、主线程和渲染线程各吃多少 CPU。不注入引擎、不改包、不需要可调试 APK。

## 支持矩阵

| 能力 | Unity | UE5（含 5.8） | 原生 / HWUI |
| --- | --- | --- | --- |
| 引擎识别 | 线程名 `UnityMain` / `UnityGfxDeviceW` | 线程名 `GameThread` / `RenderThread` / `RHIThread` | 识别为"原生" |
| 帧率来源 | SurfaceView 层的 SurfaceFlinger 时间戳；取不到退 `gfxinfo` | 同 Unity | 窗口层 + `gfxinfo` |
| 主线程 CPU | `UnityMain` 单核占比 | `GameThread` 单核占比 | 无（`—`） |
| 渲染线程 CPU | `UnityGfxDeviceW` 单核占比 | `RenderThread`（无则 `RHIThread`） | 无（`—`） |
| 卡顿帧 | `gfxinfo` Janky frames 增量 | 同 Unity | 同 Unity |
| 进程 CPU / 内存 / 温度 | `/proc` + 电池温度 | 同 Unity | 同 Unity |
| 输出 | CSV 带 `engine/main_thread/main_cpu/render_thread/render_cpu` | 同 Unity | `engine=native` |

## 识别怎么做的

1. 采样开始时读 `/proc/<pid>/task/*/comm`，拿到全部线程名。这个目录在 Android 上对普通 shell 可读，不需要 root。
2. 按线程名判定：出现 Unity 线程即 Unity；出现 `GameThread` 且配 `RenderThread`/`RHIThread` 即 UE5；都没命中记为 `native`。
3. 每 10 个采样重试一次识别，覆盖引擎线程晚启动、切场景重建线程池的情况。
4. 运行中进程重启（PID 变化）会清空识别结果重新判断。

**截断问题**：Android 的 `comm` 最多 15 字符，某些厂商截掉的是开头（实测 ColorOS 上 `com.android.settings` 变成 `ndroid.settings`）。所以线程名匹配做了"精确 + 前缀/后缀"容错，且只对 8 字符以上的名字容错，避免把普通 `main` 线程误判成 `UnityMain`。

## 线程 CPU 怎么算

- 每个采样读一遍 `/proc/<pid>/task/<tid>/stat`，取 utime+stime 的累计 jiffies。
- 单线程占比 = 本次累计差 / 100 Hz / 采样间隔 × 100%，单核口径，可超过 100 时截顶。
- 引擎主线程、渲染线程按识别结果取；找不到就留空，UI 显示 `—`，不编数。

## 帧率口径（和引擎无关的部分）

- 优先 `dumpsys SurfaceFlinger --latency <层>`：取该层最近的呈现时间戳。
- 用**两次读取之间新增的时间戳数量 / 采样间隔**算帧率。这样屏幕静止时是 0 fps，而不是把上一轮的 60 fps 当成现在。
- 新增为 0 或层丢失，退回 `gfxinfo` 的渲染帧数增量；仍无帧就标 `无新帧`。
- 缓冲最多 127 帧，所以 1 秒间隔下帧率上限按 240 截顶；144 fps 场景建议 `-interval 500ms`，这点写在 [perfdog-mode.md](perfdog-mode.md) 里。

## UE5.8 的边界（别当承诺）

- **只认线程，不认版本**：能从线程名判断是 UE，但 5.8 还是 5.4 需要看 APK/`libUnreal.so`，当前没做包解析。
- **不吃 UE 的 stat 命令**：`stat unit` / `stat fps` 需要非 Shipping 包并在控制台开启，QAWork 不注入控制台，也不解析日志。
- **Vulkan 后端不影响口径**：不管 GLES 还是 Vulkan，帧都落在 SurfaceView 层，读法一样。
- **渲染线程可能合并**：部分 UE 配置把 RHI 并到 RenderThread，此时只报 `RenderThread`；找不到就空着。

## Unity 的边界

- **Mono / IL2CPP 不影响**：两者线程名一致。
- **Job.Worker 不聚合**：只报主线程和 Gfx 设备线程，Worker 线程的合计 CPU 在 `cpu_pct`（进程总）里能看到，但没单列。
- **URP 多相机不额外拆**：帧率仍按整层呈现节奏算。

## 验收现状

- 单元测试覆盖：层名解析（含 Android 12+ 的 `RequestedLayerState{...}`）、引擎识别、线程名截断容错、线程 stat 解析、CSV 引擎列往返。
- 真机验证：Android 13 模拟器 + ColorOS 真机跑通原生应用采集；**Unity / UE 包的真机验收还没做**，等有可测包再补，验收方法写在这里，避免"看起来支持"。

## 后续

- APK 侧识别：`pm path` + `libunity.so` / `libUnreal.so` 判定，补上"没运行也能标引擎"。
- UE 的 `stat` 日志接入（需用户显式开启），拿 GameThread/RenderThread 的帧内耗时，而不只是 CPU 占比。
- Unity Profiler 通道（需开发包 + 端口转发），采集 `ProfilerRecorder` 指标。
