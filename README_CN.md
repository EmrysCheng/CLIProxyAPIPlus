> rc.3 修复升级页按钮对比度与布局，置顶显示 17 个构建检查阶段、耗时和最近活动；依赖网络失败返回脱敏原因。该版本的构建代理由操作者显式配置；rc.5 起按用户要求在随包配置中预置 Go 代理，不关闭依赖校验。

> 0.4.0-rc.6：完整安装包已内置 Python / Go / Node / npm，页面源码升级无需自行下载构建工具或配置 PATH。请保留 build-tools、update-tools.json、updater-source；默认 update-tools.json 同时包含 `go_proxy: https://goproxy.cn|https://proxy.golang.org`，可按需修改或删除；上游源码和依赖仍需联网。详见 docs/SOURCE_UPDATES_CN.md。

> **当前更新方式（0.4.0-rc.6）**：管理页面直接检查 CLIProxyAPI / CPA-Manager-Plus 的最新正式版，选择更新一个或两个项目后，在本机隔离构建并验证整合程序、安装并支持成对回滚。**不需要 release-index 或额外发行服务**；执行源码构建须显式 `--web-updates --source-updates` 和可信 Python/Go/Node/npm。完整配置先看 [双上游源码自更新](docs/SOURCE_UPDATES_CN.md)。下文历史发行服务/索引方式仅为高级兼容选项。

# CLIProxyAPIPlus

**当前交付：`0.4.0-rc.6` 候选版本，不是已经完成全部生产验收的正式版。**

将 CLIProxyAPI 核心与 CPA-Manager-Plus 管理后端、原有 React 前端编译进一个 Go 程序：**一个业务进程、一个对外业务端口**。运行时不需要 Node、Nginx 或另一份 CPA/Manager 服务。

## 功能与边界

- `/`、`/management.html` 使用 CPA-Manager-Plus 真实构建页面；保留主题、仪表盘、配置、用量与账户功能，不重新设计一套 UI。
- `/v1/...`、`/v1beta/...` 等模型协议走原始核心；管理请求统一先过 Manager 的权限及保护规则。
- 管理员、模型客户端、内部核心管理三类凭据分离；内部管理调用通过专用 `RoundTripper`，不打开第二个业务端口，不替换全局 HTTP Transport。
- Manager 独立入口和嵌入入口共用初始化、数据库锁、后台任务及有界排空；保留原始独立入口的可编译性。
- 配置热更新不能修改监听器、TLS、内部密钥、认证目录或开启额外监听器。上述设置要离线修改并受控重启。
- 便携程序内嵌 IANA 时区数据，上海等统计时区不依赖宿主 Go SDK；核心与 Manager 运行日志默认写入同一轮转文件，不再持续刷控制台。
- 页面“系统信息 → 程序更新”显示双上游版本、新版本提醒、升级进度及成对回滚记录。独立 Portable 实例显式开启后，可确认升级并重启；不分别升级 CPA、Manager 或 HTML。
- 严格锁定源码快照、可重放适配补丁、候选更新/验证/晋级、备份/迁移/恢复、升级失败成对回滚、SBOM 与许可文件汇集。

首期支持文件认证存储和本地 SQLite；Home、PG/Git/对象 token store、CLI/TUI 启动模式、mDNS、pprof **没有在合并入口提供**，不能当作与原核心所有启动模式完全等价。Portable 构建禁用动态插件；Linux Full/CGO 构建保留插件能力但仍需原生验收。OAuth 如需临时回调监听，仅绑定回环地址，不等于承诺全流程只有一个 socket。详细限制见 `docs/IMPLEMENTATION_STATUS_CN.md`。

## 本机启动

Windows 文件：`dist/windows-amd64-portable/cliproxyapi-plus.exe`。

```powershell
# 仅在全新路径初始化，不会覆盖已有目录或配置
.\dist\windows-amd64-portable\cliproxyapi-plus.exe --init --config config.yaml --data-dir .data --listen 127.0.0.1:8317
.\dist\windows-amd64-portable\cliproxyapi-plus.exe --config config.yaml --data-dir .data
```

浏览器打开 `http://127.0.0.1:8317/`，使用 `.data/admin.key` 内容登录；模型客户端使用 `.data/api.key`。`.data/core-management.key` 只供程序内部使用，**不要放入浏览器或分享**。`data.key` 是 Manager 加密数据密钥，必须与数据库一起备份。

默认只允许回环地址。要对外监听，显式配置地址并传入 `--allow-remote`，同时配置 TLS、访问控制和防火墙；不要把管理端口直接裸露到公网。Windows 初始化会设置受保护 DACL，POSIX 使用目录/文件权限。备份含认证秘密，也应按密钥管理。

## 文件日志（rc.2）

新安装及旧配置中**未指定**日志选项时，默认启用：

```yaml
logging-to-file: true
logs-max-total-size-mb: 100
```

核心访问日志（包括用量轮询）与 Manager 后台日志写入同一个 `main.log`，单文件 10 MB 轮转；总量清理由上游每分钟执行，目标 100 MB，活跃文件不删除，并非瞬时硬配额。控制台只保留必要的启动信息和致命错误。启动时会打印 `Application log: <绝对路径>`；日志文件无法打开时直接报错退出，不静默丢日志。

日志目录沿用上游规则：设置 `WRITABLE_PATH` 时为其下的 `logs`；否则优先使用当前工作目录下**已存在且可写**的 `logs`，再回退到 `auth-dir/logs`。如需固定在启动目录，可在启动前执行：

```powershell
$env:WRITABLE_PATH = (Get-Location).Path
.\dist\windows-amd64-portable\cliproxyapi-plus.exe --config config.yaml --data-dir .data
# 此时日志：当前目录\logs\main.log
```

旧配置明确设置 `logging-to-file: false` 时仍会输出到控制台，请在原配置/页面中切换为 `true`；`logs-max-total-size-mb: 0` 明确表示不设总量限制。配置热更新会同步应用于核心与 Manager 日志。升级只需受控重启新程序并沿用原 config/data 参数，**不要重新执行 `--init`**。日志可能包含请求信息，分享前请脱敏，日志目录权限应按部署环境保护。

## 页面升级、提醒与回滚（0.3）

“系统信息 → 程序更新”显示当前程序/双上游版本、发行信息、操作阶段和升级历史。配置可信产品索引后，后端启动时及每 6 小时检查一次；已登录页面显示全局升级提醒。只提醒，不自动安装。

独立运行的 Portable 实例可显式开启（请替换实际配置、数据和可信索引路径）：

```powershell
.\dist\windows-amd64-portable\cliproxyapi-plus.exe --config config.yaml --data-dir .data --web-updates --source-updates
# 高级兼容模式：若另行配置私有 HTTPS 发行源，可另加：--release-ca-file "D:\certs\release-ca.pem"
```

这不是默认启用的“任意 URL 执行器”。页面只能选择操作者索引中匹配当前平台的整合 Portable 包，并重新核对版本/摘要；需要输入目标版本确认。只接受包含网页更新协议的整合程序（0.3 起）；第一次从 0.2 升至 0.3 仍需手动替换或使用原离线工具。不会在线拉取两个 Git 仓库重新编译。

升级会短暂停机：下载/校验 → 等待请求正常排空及旧进程退出 → 一致性文件快照 → 替换程序/离线迁移 → 后台启动并检查新实例。失败则恢复匹配的程序、配置和数据。成功后的“回滚”也会恢复升级前快照；必须二次确认快照后数据变化的影响，失败/回滚前的文件会单独保留，不删除现有用户数据。

**不要与 Windows Service、systemd、容器或其他自动拉起策略混用。** 已识别的托管/容器环境禁用此能力；其他第三方守护器仍需操作者自行确认。升级后新实例在后台运行。外置 auth/TLS 材料、路径链接或不可写的安装目录请使用原离线工具。私有事务/备份在 `<data-dir>.updates`，不要在服务运行时修改或删除；定期检查磁盘容量，备份不会自动过期删除。

详细恢复方法和安全边界见 `docs/WEB_UPDATES_CN.md` 与 `docs/OPERATIONS_CN.md`。可信 HTTPS 与 SHA-256 不是发行签名，生产签名/发布审批门禁仍未完成。

## 从源码构建

构建工具：Python **3.12+**、Go **1.26.10**、Node **24**、npm。维护工具显式调用 Windows 原生安全 API 设置受保护 DACL，不依赖 `chmod` 或特定 Python 版本的 Windows 目录权限语义。

```powershell
# 首次克隆后按锁定提交获取并校验两个上游；已存在时只校验，不覆盖
python scripts/hydrate_upstreams.py
python scripts/project.py prepare
python scripts/project.py build
python scripts/project.py test-e2e
python scripts/project.py test-update
python scripts/project.py test-web
python scripts/regression.py
python scripts/smoke_binary.py
python scripts/smoke_maintenance.py
```

可指定 `--go <go.exe> --node <node.exe> --npm-cli <npm-cli.js>`。首次 `build` 执行 `npm ci --ignore-scripts`、TypeScript/Vite 构建、Python/Go 测试、二进制编译与 SBOM 汇集；`test-web` 另外执行 ESLint 和完整 Vitest。已有真实前端可用 `build --skip-web`，但不能绕过内嵌摘要校验。包不会仅包含源码占位 HTML。

```powershell
python scripts/project.py build --skip-web --target linux/amd64
python scripts/project.py build --skip-web --target linux/arm64
# Full 必须在有对应 CGO 工具链的平台构建与运行验证
python scripts/project.py build --variant full --target linux/amd64
```

产物：目标目录中的程序、`build-info.json`、`checksums.txt`、`sbom.spdx.json`、`licenses-index.json`、`compliance-report.json`，及 `dist/CLIProxyAPIPlus_<version>_<os>_<arch>_<variant>.zip|tar.gz`。`--skip-sbom` 仅用于诊断，不能因此宣称通过发行合规门禁。

`upstream/` 是不可直接修改的原始快照，**不提交到公开 Git 仓库，不要使用 `git add -f upstream`**。公开源码保存整合代码、补丁和 `upstream.lock.json`；`hydrate_upstreams.py` 仅从固定的原始 GitHub 仓库按完整提交下载，并核对整个树摘要、模块身份和 LICENSE。已有快照损坏时拒绝覆盖；`--offline` 只验证已有快照，缺失即失败。`.build/sources/` 是可丢弃的构建副本，不是新的源码来源。锁文件使用跨平台 UTF-8 POSIX 路径顺序计算摘要。完整安装包的 `updater-source` 仍保留已验证的源码配方，因此页面自更新不依赖 Git 或本机源码克隆；此分发调整不代表第三方 OAuth 常量、安全或许可审核已经通过。

公开 CI 仅按白名单导出构建身份、校验和及候选验证元数据，不自动上传二进制、完整源码、安装归档或宽泛日志/报告目录；整包再分发仍须单独完成安全与正式发布审批。

## 持续同步两个上游

```powershell
# 只读查看；stable=最新正式发布，edge=main 当前提交
python scripts/project.py check-updates --channel stable
# 隔离下载，打印本次 candidate 的绝对路径
python scripts/upstreams.py stage --project all --channel stable
# 使用上一步返回的实际目录；可传入前述工具路径
python scripts/upstreams.py validate --candidate <candidate-directory>
# 审阅差异、验证回执后再晋级源码组合，不会替换正在运行的程序
python scripts/upstreams.py promote --candidate <candidate-directory>
```

可用 `--project cliproxyapi` 或 `--project cpa-manager-plus` 单侧同步；`--channel edge` 跟踪 main；固定提交用单侧 `--ref <完整SHA或tag>`。任一快照摘要/补丁前像不匹配都失败，不做模糊应用。冲突只能修改适配层、重新 stage/validate；稳定快照不受候选失败影响。`--offline` 复用当前快照用于流程演练，不代表检查过最新远程版本。

CI 提供三平台 Portable 构建和 Linux Full/全量测试，另有每日只读权限的上游候选验证工作流。**未配置远程仓库，工作流尚未在 GitHub 执行**；没有自动提交、推送、安装或发布，也没有发布令牌。源码晋级不是生产上线审批。

## 导入、备份和升级

详见 `docs/OPERATIONS_CN.md`。维护工具默认要求停止服务，绝不直接复制仍在运行的 SQLite WAL 文件作为备份。导入是复制到新路径；缺原 `data.key` 会拒绝导入，不生成不相干的密钥来“修复”旧库。

```powershell
python scripts/data.py backup --config config.yaml --data-dir .data --snapshot backup.zip
python scripts/data.py verify --snapshot backup.zip
python scripts/data.py restore --snapshot backup.zip --config restored.yaml --data-dir restored-data
# 升级默认只是 dry-run，必须明确提供可信 SHA256 和 --apply
python scripts/upgrade.py install --binary <installed-binary> --candidate <new-binary> --sha256 <trusted-sha256> --config config.yaml --data-dir .data --state-dir upgrade-history
```

正常运行不在线编译源码。整包发行索引需操作者配置可信 HTTPS 地址（`--release-index` 或 `CLIPROXY_PLUS_RELEASE_INDEX`）；未配置时明确显示无候选，不虚构“已经是最新”。0.3 起支持显式开启的网页整包安装与成对回滚，下载/校验/备份/替换由受控的同程序辅助进程执行，而不是在浏览器执行文件。`scripts/release_index.py` 核对包摘要并从包内读取真实产品源码 SHA256；可选附加真实 Git 提交，不发布或签名。校验和不能替代可信来源或发行签名。

## 验收与证据

`docs/IMPLEMENTATION_STATUS_CN.md`、`docs/RELEASE_CHECKLIST_CN.md` 区分已验证、未运行及已知失败。`.build/*report.json` 保存本机演练结果（**不要发布含秘密的测试数据目录**）。

真实 OAuth/供应商联调、Linux 原生/Full/ARM 与容器运行、24 小时长稳、依赖许可/漏洞审阅及正式发布审批仍是门禁。上游全量测试不全绿，取消测试还在未打补丁基线复现失败；不能把本机单端口验证扩大解释为全协议和生产验收。


### 本机 HTTP 更新源（0.3.0-rc.2 起）

无需公网域名或本机 TLS 证书。显式使用 `--web-updates --allow-local-release-http --release-index "http://127.0.0.1:26973/index.json"`；HTTP 仅允许回环 IP，同源包与重定向，默认 HTTPS 策略不变。发行源需另行准备，不能只填一个地址。完整的**生成索引 → 启动本机静态服务 → 页面提醒/升级/回滚**步骤见 [本地网页升级配置](docs/LOCAL_WEB_UPDATES_CN.md)。旧 rc.1 首次需手动换成支持新参数的程序；相同版本不会产生升级提醒。


### 独立本地发行服务便携包（0.3.0-rc.3）

`CLIProxyAPIPlus_ReleaseServer_<版本>_<平台>_portable.zip/.tar.gz` 提供自包含的 Go 本机发行服务、JSON 配置、启动脚本、索引和已验证的整合安装包；运行时无须 Python/Node/Go。Windows 解压运行 `start.cmd`，默认提供 `http://127.0.0.1:26973/index.json`，日志落盘轮转。只读、回环监听、不暴露目录/配置/日志。构建入口为 `python scripts/project.py build-release-server --target windows/amd64`（先完成对应产品构建）；完整配置与升级方式见 [发行服务说明](docs/RELEASE_SERVER_CN.md)。
