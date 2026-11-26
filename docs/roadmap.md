# 路线图

原则：先把 PerfDog 性能模式的口径和门禁做准，再扩模式；每个阶段都要有可跑的验收命令，不靠截图吹进度。

## v0.1（已落地）

- 原生窗口（MyGo + Lobe 主题），默认 PerfDog 性能模式
- Android 采样：SurfaceFlinger 帧率、gfxinfo 卡顿、`/proc` CPU/内存、电池温度
- 本机进程采样：CPU + 内存（明确标注无帧率）
- CSV 落盘、`qawork perf` / `qawork report` 门禁退出码
- 首帧自检 `-capture`，CI 在 windows-latest 跑 vet/test/build

## v0.2（下一步）

- 帧率稳定性：长时采集的掉帧归因（卡顿帧 × 时间轴对齐）
- 采集前自动识别 `SurfaceView` / Unity / Unreal 层命名
- 报告页：单次报告可导出 Markdown（含曲线图），进 PR 附件
- 阈值可从 `qawork.yaml` 读，CLI 与 GUI 共用

验收：`qawork perf -duration 120s` 在两台不同 Android 设备上给出可复现 CSV；静态界面的 0 fps 有明确标注。

## v0.3

- 兼容性矩阵：多设备并发安装/冷启/首帧耗时，结果本地 SQLite
- 崩溃与 ANR 统计（`logcat` 结构化），矩阵视图

验收：3 台设备并行跑同一 APK，输出一张矩阵表 + 失败用例的 logcat 片段。

## v0.4

- `qawork.yaml` 用例 + adb 回放 + 性能门禁，跑完生成 CI 产物
- GitHub Actions 模板，开箱即用

## 暂不做

- Present hook / ETW 的 PC 游戏帧率采集（工程量大，先不画饼）
- 云真机出租、账号体系、云端协作（那是 WeTest 的主场，不是 QAWork 的方向）
