# 自动化与兼容矩阵

两条线共用一个东西：**adb 在真设备上真的跑，结果真的落盘**。自动化回答"这个场景在指定机型上过不过"，兼容回答"同一场景在一批机型上有没有崩/卡死"。

## 1. qawork.yaml

```yaml
version: 1
defaults:
  serial: ""          # 留空取第一台在线设备
  interval: 1s        # 200ms–10s
  warmup: 1s          # 0–60s，启动后先等再执行步骤
  settle: 1s          # 0–60s，步骤跑完再采一会儿
  assert:
    min_fps: 48
    max_jank: 5
    max_cpu: 70
    max_mem: 1024
    max_temp_c: 45
    max_frame_p95_ms: 33
    max_startup_ms: 5000
cases:
  - name: login
    package: com.example.game
    activity: ""       # 留空自动 resolve-activity
    steps:
      - {action: launch, cold: true}
      - {action: tap, x: 540, y: 1600}
      - {action: wait, seconds: 8}
      - {action: swipe, x: 540, y: 1600, x2: 540, y2: 600, duration_ms: 400}
      - {action: screenshot}
```

- `version` 只支持 1。
- 一个计划最多 100 个 case、每个 case 最多 100 步；case 名不能重复。
- 阈值是 `*float64`：**不写=继承 defaults**，**显式写 0=关闭该项检查**。`defaults.assert` 也可以显式关。
- `interval` 最小 200ms；低于这个值 adb 采样本身会互相踩。
- `serial` 优先级：CLI/API 覆盖 > case > defaults > 第一台在线设备。

### 步骤白名单

| action | 必填 | 说明 |
| --- | --- | --- |
| `launch` | — | 只能第一步；`cold` 默认 true，先 `am force-stop` 再 `am start -W`。解析不到 activity 时退回 `monkey`，且**不假装测到启动耗时** |
| `wait` | `seconds` 或 `ms` | 最多 120s |
| `tap` | `x`, `y` | `input tap` |
| `swipe` | `x,y,x2,y2` | `duration_ms` 默认 300，上限 5000 |
| `keyevent` | `key` | `input keyevent` |
| `text` | `text` | 只支持 ASCII；空格转 `%s`；`adb input text` 不收中文 |
| `screenshot` | — | 存 `step-NN.png` |

`launch` 只能在第一步：采样器启动后再强退应用会让采样的 PID 失效。校验器会拦。

### 执行与产物

```bash
qawork run -plan qawork.yaml -validate
qawork run -plan qawork.yaml -serial 127.0.0.1:16384 -artifacts out
```

每个 case 独立目录 `<artifacts>/<case>-<timestamp>/`：

- `samples.csv`：与 `perf` 完全同一份 14 列口径。
- `result.json`：case、serial、package、startup_ms、steps[]、summary、checks[]、failed、error。
- `step-NN.png`：`screenshot` 步骤。
- `failure.png`：任何失败自动截一张，截不到就不写路径，不假造。

退出码：通过 0；门禁失败 2；执行/参数错误 1（与 `compat` 有失败行时退出 2 一致）。GUI 里不退出进程，只显示失败行。

## 2. 兼容矩阵

```bash
qawork compat -plan qawork.yaml -apk app.apk
qawork compat -package com.example.game          # 无 plan，只做启动 smoke
qawork compat -plan qawork.yaml -devices 127.0.0.1:16384
```

对每台在线设备：

1. 采集 `ro.product.model` / `ro.build.version.release` / `ro.build.version.sdk` / `ro.product.cpu.abi` / `wm size`。
2. 有 `-apk` 就 `adb install -r -g`，失败直接记该设备所有 case 失败。
3. 每个 case 前 `logcat -c`，跑同一个 `automation` case（serial 覆盖）。
4. 跑完 `logcat -d -v brief -t 2000`，只在 **包名与事件同一条/相邻行** 时判 crash/ANR：`ANR in <pkg>`，或 `FATAL EXCEPTION` 后 5 行内出现 `Process: <pkg>`。别的 App 崩了不算。
5. 每台设备额外截一张 `device.png`，logcat 原文落 `logcat.txt`。
6. 汇总 `compat-matrix.csv` / `compat-matrix.json`。

没有 plan 时用内置 smoke：冷启动 + 等 5 秒 + 全部性能阈值置 0。它只证明"能装、能启、没崩"，FPS/CPU 只做参考，不做门禁。要性能结论必须给 plan。

### 矩阵字段

`serial, model, android, sdk, abi, resolution, case, package, installed, install_skipped, startup_ms, fps_mean, fps_min, frame_p95_ms, jank_pct, cpu_max, mem_max_mb, temp_max, failed_checks, crash, anr, passed, error, csv, logcat, screenshot, artifacts`

`install_skipped=true` 表示没给 APK、没有执行安装，不代表应用一定已装；`installed=true` 只在真的执行安装且返回 `Success` 时成立。

`passed` 的条件：安装成功、case 没有执行错误、没有失败检查、没有 crash/ANR。性能采样为 0（例如设备没有帧来源）不会单独判失败，但会体现在空值/0 上。

### 已知边界

- 一次只扫 **last 2000 行** logcat；长跑崩溃可能被刷掉，需要长稳就缩短单 case 或另接 `logcat` 流。
- 只扫 Java `FATAL EXCEPTION` 与 ANR；native tombstone、低内存杀进程不在这一版。
- 多台设备是顺序跑，不是并行；USB 带宽和 adb 串行更稳。
- 兼容矩阵的真机多机型验证仍需硬件；仓库验证记录只有 Windows + 一台模拟器/一台真机时写的，不冒充全机型结论。
- 宿主只支持 Windows amd64 / macOS arm64；Android 设备侧不区分宿主。

## 3. 已验证样例

2026-11-26，Windows 11 + `127.0.0.1:16384`（BVL-AN16，Android 15）跑 `examples/qawork.yaml` 的 Settings 场景：7 步全部执行，启动 1224ms，平均 33.7 FPS、卡顿 0.4%、CPU 7%、内存 172MB，门禁通过，CSV/JSON/截图落盘。兼容 smoke 同设备：启动 637ms、30 FPS、无 crash/ANR、矩阵通过。

这些数字只证明链路通，不是性能基线。
