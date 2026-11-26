# 自动化与兼容矩阵

自动化、兼容矩阵、压测三条线共用一个东西：**adb 在真设备上真的跑，结果真的落盘**。自动化回答"这个场景在指定机型上过不过"，兼容回答"同一场景在一批机型上有没有崩/卡死"，压测回答"长随机输入下稳不稳"。

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
- 一个计划最多 100 个 case、每个 case 最多 200 步；case 名不能重复。
- 阈值是 `*float64`：**不写=继承 defaults**，**显式写 0=关闭该项检查**。`defaults.assert` 也可以显式关。
- `interval` 最小 200ms；低于这个值 adb 采样本身会互相踩。
- `serial` 优先级：CLI/API 覆盖 > case > defaults > 第一台在线设备。

### 步骤白名单

| action | 必填 | 说明 |
| --- | --- | --- |
| `launch` | — | 只能第一步；`cold` 默认 true，先 `am force-stop` 再 `am start -W`。解析不到 activity 时退回 `monkey`，且**不假装测到启动耗时** |
| `wait` | `seconds` 或 `ms` | 最多 120s |
| `tap` | `x`, `y` | `input tap` |
| `longpress` | `x`, `y` | `input swipe x y x y duration_ms`；默认 600ms |
| `doubletap` | `x`, `y` | 两次 `input tap`，间隔 120ms |
| `swipe` | `x,y,x2,y2` | `duration_ms` 默认 300，上限 5000 |
| `drag` | `x,y,x2,y2` | 带时长 swipe；默认 800ms |
| `keyevent` | `key` | `input keyevent` |
| `text` | `text` | 只支持 ASCII；空格转 `%s`；`adb input text` 不收中文 |
| `stop` | — | `am force-stop` case 包名 |
| `clear` | — | `pm clear` case 包名 |
| `grant` / `revoke` | `permissions[]` | 校验权限名，逐条 `pm grant/revoke` |
| `wait_activity` | `activity` 或 `text` | 500ms 轮询当前 activity，默认 10s，上限 120s |
| `wait_text` | `text` | 500ms 轮询 uiautomator 可见文本 |
| `assert_activity` | `activity` 或 `text` | 当前 activity 不匹配直接失败 |
| `assert_text` / `assert_not_text` | `text` | 可见文本存在/不存在；依赖 accessibility tree |
| `assert_package` | — | `pidof` case 包名，进程不在就失败 |
| `screenshot` | — | 存 `step-NN.png` |

`launch` 只能在第一步：采样器启动后再强退应用会让采样的 PID 失效。校验器会拦。`wait_text` / `assert_text` 读的是 uiautomator dump，SurfaceView 游戏 UI 可能读不到文本，这是已知边界，不当作通过。

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
qawork compat -plan qawork.yaml -repeat 3 -reset # 每台设备每个 case 跑 3 轮，每轮前 pm clear
```

对每台在线设备、每个 case、每个重复轮次：

1. 采集 `ro.product.model` / `ro.build.version.release` / `ro.build.version.sdk` / `ro.product.cpu.abi` / `wm size`。
2. 有 `-apk` 就 `adb install -r -g`，失败直接记该设备所有 case 失败。
3. 每轮前可选 `pm clear`（`-reset`），再 `logcat -c`，跑同一个 `automation` case（serial 覆盖）。
4. 跑完 `logcat -d -v brief -t 2000`，只在 **包名与事件同一条/相邻行** 时判 crash/ANR：`ANR in <pkg>`，或 `FATAL EXCEPTION` 后 5 行内出现 `Process: <pkg>`。别的 App 崩了不算。
5. 每台设备额外截一张 `device.png`，logcat 原文落 `logcat.txt`；重复轮次的自动化产物在 `run-XX/`。
6. 汇总 `compat-matrix.csv` / `compat-matrix.json` / `compat-report.html` / `compat-junit.xml`。

没有 plan 时用内置 smoke：冷启动 + 等 5 秒 + 全部性能阈值置 0。它只证明"能装、能启、没崩"，FPS/CPU 只做参考，不做门禁。要性能结论必须给 plan。

### 矩阵字段

`serial, model, android, sdk, abi, resolution, case, run, package, installed, install_skipped, reset, startup_ms, fps_mean, fps_min, frame_p95_ms, jank_pct, cpu_max, mem_max_mb, temp_max, failed_checks, crash, anr, passed, error, csv, logcat, screenshot, artifacts`

`run` 从 1 开始；`reset=true` 表示该轮前真的执行了 `pm clear`。`install_skipped=true` 表示没给 APK、没有执行安装，不代表应用一定已装；`installed=true` 只在真的执行安装且返回 `Success` 时成立。

`passed` 的条件：安装成功、case 没有执行错误、没有失败检查、没有 crash/ANR。性能采样为 0（例如设备没有帧来源）不会单独判失败，但会体现在空值/0 上。

HTML 报告是单文件，含设备/系统/case/轮次/安装/启动/FPS/CPU/内存/crash/ANR/结果和证据相对链接；JUnit 每个矩阵行一个 `testcase`，失败带 `<failure>`，可直接给本地测试面板或任意 JUnit 消费端。

## 3. 压力测试

```bash
qawork stress -package com.example.game -serial 127.0.0.1:16384 -duration 30m
qawork stress -package com.example.game -devices a,b -events 5000 -throttle 200
```

流程：

1. warm launch 目标包（不等同于 `automation` 的冷启动门禁）。
2. 启动 Android 采样器，按 `interval` 采 FPS/CPU/内存/温度。
3. `monkey -p <pkg> --throttle <ms> -v <events>` 长随机输入；时长上限 2h，事件上限 100 万。
4. 停止采样，读最后 5000 行 logcat，按包名判 crash/ANR。
5. 落 `samples.csv`、`result.json`、`logcat.txt`、`monkey.txt`；超时被杀的 monkey 不当作错误。

`exit 2` 表示 crash/ANR/没采到样本/monkey 失败；`exit 1` 是参数或设备准备错误。

## 4. 已知边界

- 兼容矩阵一次只扫 **last 2000 行** logcat；长跑崩溃可能被刷掉，需要长稳就用 `stress`（扫 5000 行）。
- 只扫 Java `FATAL EXCEPTION` 与 ANR；native tombstone、低内存杀进程不在这一版。
- 多台设备是顺序跑，不是并行；USB 带宽和 adb 串行更稳。
- 文本等待/断言依赖 uiautomator accessibility tree；SurfaceView 游戏读不到时不会伪造成功。
- 兼容矩阵的真机多机型验证仍需硬件；仓库验证记录只有 Windows + 一台模拟器/一台真机时写的，不冒充全机型结论。
- 宿主只支持 Windows amd64 / macOS arm64；Android 设备侧不区分宿主。

## 5. 已验证样例

2026-11-26，Windows 11 + `127.0.0.1:16384`（BVL-AN16，Android 15）跑 `examples/qawork.yaml` 的 Settings 场景：7 步全部执行，启动 1224ms，平均 33.7 FPS、卡顿 0.4%、CPU 7%、内存 172MB，门禁通过，CSV/JSON/截图落盘。兼容 smoke 同设备：启动 637ms、30 FPS、无 crash/ANR、矩阵通过。

2026-11-27，Windows 11 + `emulator-5554`（sdk_gphone64_x86_64，Android 14，x86_64）跑深步骤 `launch → wait → wait_activity → assert_package → assert_text → screenshot`：6 步全过，启动 488ms；同设备兼容 `-repeat 2 -reset` 两轮通过，启动 440/511ms；20s monkey 压测采到 6 个样本、无 crash/ANR。模拟器帧率只有 25–28 FPS，不能当性能基线。

这些数字只证明链路通，不是性能基线。
