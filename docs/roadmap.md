# 路线图

原则：先把 PerfDog 性能模式的口径和门禁做准，再扩模式；每个阶段都要有可跑的验收命令，不靠截图吹进度。

## v0.1（已落地）

- 原生窗口（MyGo + Lobe 主题），默认 PerfDog 性能模式
- Android 采样：SurfaceFlinger 帧率、gfxinfo 卡顿、`/proc` CPU/内存、电池温度
- 本机进程采样：CPU + 内存（明确标注无帧率）
- CSV 落盘、`qawork perf` / `qawork report` 门禁退出码
- 首帧自检 `-capture`，CI 在 windows-latest 跑 vet/test/build

## v0.2（已落地）

- Unity / UE5 引擎识别（线程名），主线程与渲染线程 CPU 进 CSV/UI/CLI
- SurfaceFlinger 时间戳增量算 FPS，gfxinfo 兜底，静态界面不再把上一轮帧率当现在
- Agent 接口：CLI `-json`、`qawork mcp`（stdio MCP）
- 环境安装独立 UI + `qawork setup`：adb 必装、scrcpy 可选、驱动手动，只写用户目录与 PATH
- 单测覆盖解析器、CSV 兼容、门禁、MCP 协议、setup 解压/PATH

## v0.3（已落地）

- `qawork.yaml` 自动化：launch / tap / swipe / wait / keyevent / text / screenshot，全程采样 + 门禁 + CSV/JSON/失败截图
- `qawork run` CLI + MCP `qawork_validate_plan` / `qawork_run_plan`；GUI 自动化面板
- 批量兼容矩阵：多设备安装 APK、设备信息、冷启、短采样、logcat crash/ANR、截图、矩阵 CSV/JSON
- `qawork compat` CLI + MCP `qawork_compat_matrix`；GUI 兼容面板
- 宿主收敛：Windows amd64 + macOS arm64；macOS 本机进程 `ps` 采样、platform-tools/scrcpy 下载、`~/.zprofile` PATH
- 测试：automation 计划/步骤/launch/runner（fake device+sampler）、compat logcat/WM size/CSV、MCP 七工具、双平台测试编译
- 已真实跑通：Windows + Android 15 模拟器上的 Settings 自动化与兼容 smoke

## v0.4（下一步）

- Unity / UE 包的真机验收（当前只有线程名单测，没有可测包）
- 兼容矩阵扩展：native tombstone、低内存杀进程、长稳 logcat 流；设备并行（当前串行）
- 长时采集的掉帧归因（卡顿帧 × 时间轴对齐）
- 报告页：单次报告可导出 Markdown（含曲线图），进 PR 附件
- GitHub Actions 模板，开箱即用

验收：`qawork compat -plan qawork.yaml -apk app.apk` 在 ≥3 台真机上给出矩阵，含至少一次真实 crash/ANR 的可复现证据；Unity/UE 包的引擎与线程字段人工核对通过。

## 暂不做

- Present hook / ETW 的 PC 游戏帧率采集（工程量大，先不画饼）
- 云真机出租、账号体系、云端协作（那是 WeTest 的主场，不是 QAWork 的方向）
- iOS、Linux 宿主
