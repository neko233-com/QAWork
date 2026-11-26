# 给 Agent 用：CLI 与 MCP

QAWork 不只有窗口。CLI 和 MCP 走同一套 `internal/perf` 采样器、同一份 CSV 格式，Agent 在终端里调出来的数字和 GUI 里看到的一致，不存在"给 agent 特供"的第二套实现。

## 两种接法

| 接法 | 适合 | 入口 |
| --- | --- | --- |
| CLI | 脚本、CI、Agent 直接跑命令 | `qawork.exe devices / perf / report / setup` |
| MCP | Claude Desktop、Cursor、Codex 等支持 MCP 的宿主 | `qawork.exe mcp`（stdio JSON-RPC 2.0） |

MCP 宿主配置示例（把路径换成实际 exe）：

```json
{
  "mcpServers": {
    "qawork": {
      "command": "D:\\Tools\\qawork.exe",
      "args": ["mcp"]
    }
  }
}
```

## MCP 工具

| 工具 | 参数 | 返回 |
| --- | --- | --- |
| `qawork_doctor` | 无 | `environment`（adb/scrcpy/USB 驱动状态）、`adb_usable`、`next` |
| `qawork_devices` | 无 | `count`、`devices`（serial/state/model） |
| `qawork_perf` | `package` 必填；`serial`、`duration_seconds`(≤120)、`interval_ms`、`csv`、`thresholds` | `summary`、`samples`、`checks`、`failed`、`csv` |
| `qawork_report` | `csv` 必填；`thresholds` | `summary`、`checks`、`failed` |

`thresholds` 可传 `min_fps`、`max_jank`、`max_cpu`、`max_mem`、`max_temp_c`，只覆盖实际传入的项。

MCP 的 `tools/call` 永远返回 `content` + `structuredContent`；工具执行失败时 `isError=true`，不会用假数据兜底。协议支持 `initialize` / `ping` / `tools/list` / `tools/call`，stdout 只走 JSON-RPC，日志都在 stderr。

## CLI 约定

```powershell
qawork devices -json
qawork perf -serial 127.0.0.1:16384 -package com.example.game -duration 10s -csv run.csv -json
qawork report -csv run.csv -json
qawork setup check -json
qawork setup install -id adb
qawork mcp
```

- `perf` / `report` 门禁不通过退出码 **2**，采样/参数错误退出码 **1**，正常 **0**。
- `setup check` 必装项缺失时退出码 **2**，方便 CI/Agent 先修环境。
- `-json` 输出机器可读结果；人类可读输出在 stderr 或默认文本模式。

`perf -json` 的字段：

```json
{
  "target": "Android 127.0.0.1:16384 com.example.game",
  "elapsed_seconds": 10.2,
  "summary": {
    "samples": 9, "fps_mean": 57.3, "fps_min": 42.0,
    "frame_p50": 17.4, "frame_p95": 23.8, "frame_p99": 30.1,
    "jank_pct": 1.2, "cpu_max": 41.0, "cpu_mean": 28.0,
    "mem_max_mb": 512.0, "temp_max": 39.0,
    "engine": "unity", "engine_label": "Unity",
    "main_thread": "UnityMain", "render_thread": "UnityGfxDeviceW",
    "main_cpu_max": 33.0, "render_cpu_max": 19.0
  },
  "checks": [{"name": "平均帧率", "value": 57.3, "limit": 48, "unit": "fps", "ok": true}],
  "failed": false,
  "csv": "run.csv"
}
```

## Agent 排查流程（Android 优先）

1. `qawork_doctor`：adb 不可用就先修环境，别拿"没设备"当性能结论。
2. `qawork_devices`：拿序列号；不要猜 `emulator-5554`。
3. 确认目标在前台：`adb -s <serial> shell pidof <pkg>`，没进程就 `monkey -p <pkg> 1` 或让用户启动。
4. `qawork_perf` 采 10–30 秒；测高刷用 `interval_ms=500`，1 秒间隔会被 SurfaceFlinger 的 127 帧缓冲压低上限。
5. 看 `summary.engine`：Unity 看 `main_cpu_max` / `render_cpu_max`；UE5 看 `GameThread` / `RenderThread`。
6. 门禁失败用 `qawork_report` 换阈值复判，不要重采一遍。
7. 需要证据就落 CSV，路径由调用方给；QAWork 不自动上传任何数据。

## 能力边界（Agent 必须知道）

- **PC 进程没有帧率**：`serial=local` 只给 CPU/内存，`FrameSamples=0`，帧率门禁自动跳过并写明原因。
- **SurfaceFlinger 缓冲 127 帧**：1 秒间隔下 FPS 上限按 240 截顶，120/144 fps 请用 500 ms。
- **UE5.8 只到线程口径**：不解析 `stat unit`、不探测引擎版本、不 hook 引擎；线程改名就识别不到。
- **不提供任意 shell 工具**：MCP 只暴露上面四个工具，避免 Agent 在用户机器上执行任意 adb 命令。
- **温度是电池温度**，不是 SoC；热节流要看 thermalservice（未接）。

## 自检

```powershell
# 协议层冒烟：initialize + tools/list
'{"jsonrpc":"2.0","id":1,"method":"initialize"}' | .\qawork.exe mcp
# 有设备时：端到端采 5 秒
'{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"qawork_perf","arguments":{"package":"com.android.settings","duration_seconds":5}}}' | .\qawork.exe mcp
```

协议实现和工具参数有单测覆盖（`internal/mcp`），采样路径需要真机，单测用 pkg 参数校验和 CSV 复判兜住。
