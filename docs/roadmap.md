# 路线图

原则：先把 PerfDog 性能模式的口径和门禁做准，再扩模式；每个阶段都要有可跑的验收命令，不靠截图吹进度。

## v0.1（已落地）

- 原生窗口（MyGo + Lobe 主题），默认 PerfDog 性能模式
- Android 采样：SurfaceFlinger 帧率、gfxinfo 卡顿、`/proc` CPU/内存、电池温度
- 本机进程采样：CPU + 内存（明确标注无帧率）
- CSV 落盘、`qawork perf` / `qawork report` 门禁退出码
- 首帧自检 `-capture`；历史版本使用过 windows-latest CI，v0.4 起已移除

## v0.2（已落地）

- Unity / UE5 引擎识别（线程名），主线程与渲染线程 CPU 进 CSV/UI/CLI
- SurfaceFlinger 时间戳增量算 FPS，gfxinfo 兜底
- Agent 接口：CLI `-json`、`qawork mcp`（stdio MCP）
- 环境安装独立 UI + `qawork setup`
- 单测覆盖解析器、CSV、门禁、MCP 协议、setup 解压/PATH

## v0.3（已落地）

- `qawork.yaml` 自动化：基础步骤 + 采样 + 门禁 + CSV/JSON/失败截图
- `qawork run` CLI + MCP 工具；GUI 自动化面板
- 批量兼容矩阵：安装 APK、设备信息、冷启、短采样、crash/ANR、截图、CSV/JSON
- `qawork compat` CLI + GUI 兼容面板
- 宿主收敛：Windows amd64 + macOS arm64
- 已真实跑通 Windows + Android Settings 自动化与兼容 smoke

## v0.4（已落地）

- `internal/adbkit`：显式 adb 能力层，设备信息、安装/启停/清数据/权限、截图、logcat、UI dump、monkey，不做任意 shell
- 自动化深步骤：longpress / doubletap / drag / stop / clear / grant / revoke / wait_activity / wait_text / assert_activity / assert_text / assert_not_text / assert_package；步骤上限 200
- 兼容矩阵：`-repeat` 重复轮次、`-reset` 每轮 `pm clear`、`compat-report.html`、`compat-junit.xml`
- `internal/stress` + `qawork stress`：monkey 长随机输入 + 采样 + crash/ANR + 证据
- `qawork device` CLI：info/install/uninstall/start/stop/clear/grant/revoke/screenshot/logcat/monkey/animations/wake/unlock/push/pull
- MCP 扩到 11 工具：新增 device_info / logcat / app / stress
- GUI 第 4 模式「压测」
- **移除 GitHub Actions**：删除 `.github/workflows`；新增 `scripts/verify.ps1` / `scripts/release.ps1`，本地构建 Windows amd64 + macOS arm64 并 `gh release create`
- 真实跑通：Windows 11 + `emulator-5554`（Android 14 x86_64）深步骤 6 步、兼容 repeat 2 轮、20s monkey 压测

## v0.5（下一步）

- Unity / UE 包的真机验收（当前只有线程名单测，没有可测包）
- 兼容矩阵扩展：native tombstone、低内存杀进程、长稳 logcat 流；设备并行（当前串行）
- 压测增强：可取消、崩溃自动留 trace/截图、按时间轴对齐掉帧
- 报告：Markdown 导出（含曲线图），进 PR/Release 附件
- 依旧**禁止 GitHub Actions**；所有构建/发布留在本地脚本

验收：`qawork compat -plan qawork.yaml -apk app.apk -repeat 3 -reset` 在 ≥3 台真机上给出矩阵，含至少一次真实 crash/ANR 的可复现证据；Unity/UE 包的引擎与线程字段人工核对通过。

## 暂不做

- Present hook / ETW 的 PC 游戏帧率采集（工程量大，先不画饼）
- 云真机出租、账号体系、云端协作
- iOS、Linux 宿主
