# CLIProxyAPIPlus

将 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 与 [CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus) 在源码层面整合：**一个 Go 程序、一个对外业务端口，使用 CPA-Manager-Plus 原有前端**。

> **当前版本：`0.4.1`。** [Portable 发行页](https://github.com/EmrysCheng/CLIProxyAPIPlus/releases/tag/v0.4.1) · [本次发行说明](docs/releases/v0.4.1.md)。Windows amd64、Linux amd64、Linux arm64 使用不同的完整程序包；请下载下表中的 Portable 附件，不要下载 GitHub 自动生成的 Source code 归档作为安装包。正式版本号不表示所有真实业务与生产门禁已验收，范围见[验收清单](docs/RELEASE_CHECKLIST_CN.md)。

[更新记录](CHANGELOG.md) · [使用指南](docs/USER_GUIDE_CN.md) · [页面升级与回滚](docs/SOURCE_UPDATES_CN.md) · [开发与构建](docs/DEVELOPMENT_CN.md) · [文档目录](docs/README.md)

## 功能概览

- **同端口访问**：管理页面、Manager 管理接口与模型协议共用一个业务入口，无需另启 Manager、Node 服务或 Nginx。
- **保留原有界面**：使用实际构建并嵌入的 CPA-Manager-Plus React 前端，提供配置、账户、用量、模型价格等页面。
- **双上游页面更新**：分别检查两个官方项目的正式版本，手动确认单独或一起更新，在本机隔离构建、验证后安装；支持程序和数据成对回滚。
- **内置更新工具**：完整 Portable 包携带 Python、Go、Node、npm，无需用户自行安装 SDK；更新源码及依赖仍需要网络。
- **凭据与数据隔离**：管理员、模型客户端、内部核心管理凭据分离；本地 SQLite、认证文件及加密密钥支持离线一致性备份。
- **文件日志**：核心与 Manager 默认写入共享轮转日志，控制台保留必要启动信息及致命错误。
- **可追踪构建**：固定上游提交、树摘要、严格补丁、构建身份和源码配方，不直接追随未经验证的上游代码。

```text
浏览器 / 模型客户端
         │  同一个业务端口（示例 8317）
         ▼
  CLIProxyAPIPlus
    ├─ CPA-Manager-Plus 前端与管理后端
    ├─ CLIProxyAPI 核心与模型协议
    └─ SQLite / 认证文件 / 私有更新记录
```

| 用途 | 默认本机入口 | 凭据 |
| --- | --- | --- |
| 管理页面 | `http://127.0.0.1:8317/`、`/management.html` | Manager 管理员凭据 |
| OpenAI 兼容 API | `http://127.0.0.1:8317/v1` | 配置中的客户端 API key |
| Gemini 兼容 API | `http://127.0.0.1:8317/v1beta` | 按核心协议使用客户端 API key |
| 就绪 / 版本 | `/plus-api/ready`、`/plus-api/version` | 公开的非秘密诊断信息 |
| 页面源码更新 | `http://127.0.0.1:8317/#/system/updates` | Manager 管理员凭据 |

“一个端口”指常驻业务入口；OAuth 流程和更新验证可能临时使用回环回调或测试端口。

## 快速开始

### 1. 准备完整 Portable 目录

| 操作系统 / 架构 | Portable 下载 | 适用设备 |
| --- | --- | --- |
| Windows x64（amd64） | [CLIProxyAPIPlus_0.4.1_windows_amd64_portable.zip](https://github.com/EmrysCheng/CLIProxyAPIPlus/releases/download/v0.4.1/CLIProxyAPIPlus_0.4.1_windows_amd64_portable.zip) | 64 位 Intel / AMD Windows |
| Linux x64（amd64） | [CLIProxyAPIPlus_0.4.1_linux_amd64_portable.tar.gz](https://github.com/EmrysCheng/CLIProxyAPIPlus/releases/download/v0.4.1/CLIProxyAPIPlus_0.4.1_linux_amd64_portable.tar.gz) | `uname -m` 为 `x86_64` |
| Linux ARM64（arm64） | [CLIProxyAPIPlus_0.4.1_linux_arm64_portable.tar.gz](https://github.com/EmrysCheng/CLIProxyAPIPlus/releases/download/v0.4.1/CLIProxyAPIPlus_0.4.1_linux_arm64_portable.tar.gz) | `uname -m` 为 `aarch64` / `arm64` |

本次支持上述三个目标，不包含 macOS、Windows ARM64、Linux 32 位或 Alpine/musl；Linux 内置构建工具要求 glibc 2.28+。请按系统和 CPU 架构选择，不要把 Windows 包复制到 Linux。

同时下载对应的 `.sha256` 附件或 `SHA256SUMS`。Windows 使用 `Get-FileHash .\CLIProxyAPIPlus_0.4.1_windows_amd64_portable.zip -Algorithm SHA256`，与附件中的 64 位摘要逐字比对；Linux 将所选包与同名 `.sha256` 放在同一目录，执行 `sha256sum -c CLIProxyAPIPlus_0.4.1_linux_amd64_portable.tar.gz.sha256`（ARM64 替换架构名）。摘要一致后再解压。Linux 执行 `tar -xzf <包名>.tar.gz -C <新建的安装目录>`；解压保留执行权限。

从受信任渠道取得**完整匹配的安装包**，或按[源码构建指南](docs/DEVELOPMENT_CN.md)生成。保留程序旁的 `build-tools/`、`update-tools.json`、`updater-source/`，不要只复制 EXE。以下命令均在解压后的安装目录执行。

### 2. 仅在全新目录初始化并启动

Windows PowerShell：

```powershell
# 仅首次执行；config.yaml 和 data 必须尚不存在
.\cliproxyapi-plus.exe --init --config config.yaml --data-dir data --listen 127.0.0.1:8317

# 常规运行；显式允许页面源码升级与回滚
.\cliproxyapi-plus.exe --config config.yaml --data-dir data --web-updates --source-updates
```

Linux：

```sh
./cliproxyapi-plus --init --config config.yaml --data-dir data --listen 127.0.0.1:8317
./cliproxyapi-plus --config config.yaml --data-dir data --web-updates --source-updates
```

不需要页面执行更新时，去掉 `--web-updates --source-updates`。这两个参数仅用于**独立运行的 Portable 实例**，不要与 Windows Service、systemd、容器或其他自动拉起机制混用。

### 3. 登录并接入模型

1. 打开 `http://127.0.0.1:8317/`，使用 `data/admin.key` 中的初始管理员凭据登录。此文件是初始化提示，不会覆盖日后在 Manager 中修改的管理员凭据。
2. 在页面配置供应商、认证文件或 OAuth；初始化不会自带可调用的模型账户。
3. 模型客户端填写 Base URL `http://127.0.0.1:8317/v1`，使用 `data/api.key` 中的初始客户端 key，或配置中后来添加的有效 key。
4. 若费用估算一直为零，先到 `/#/model-prices` 配置价格及模型别名；缺少价格不代表模型免费。

**已有实例不要再次 `--init`，不要删除或重新生成密钥。** 默认只监听回环地址；远程访问需显式 `--allow-remote`，并配置 TLS、访问控制及防火墙，详见[使用指南](docs/USER_GUIDE_CN.md)。

## 页面升级、回滚与网络配置

推荐启动方式就是上面的 `--web-updates --source-updates`。登录后打开“系统信息 → 程序更新”，检查两个上游版本，确认目标及源码执行授权后升级。**不需要 `--release-index`、`index.json` 或额外发行服务。**

随包 `update-tools.json` 默认内容：

```json
{
  "bundle": "./build-tools",
  "go_proxy": "https://goproxy.cn|https://proxy.golang.org"
}
```

- `bundle` 相对该 JSON 文件所在目录解析；不要用空的 example 配置覆盖它。
- 非空 `go_proxy` 优先于启动环境 `GOPROXY`；删除或置空此字段才继承环境。包内 SDK 使用 `GOENV=off`，`go env -w` 不是此模式的配置入口。
- 页面显示构建阶段、耗时和最近活动，阶段数不是耗时百分比，不提供剩余时间保证。
- 回滚恢复匹配的旧程序和数据快照，**不会合并快照之后新增的业务数据**。操作前保留独立备份。
- 更新两个上游不等于获取本整合项目未来的全部产品改动；整合产品换版仍需完整、匹配且受信任的发行物。

完整流程、工具校验及失败恢复见[双上游源码自更新](docs/SOURCE_UPDATES_CN.md)。旧发行索引模式仅为[高级兼容选项](docs/WEB_UPDATES_CN.md)。

## 从源码构建

开发环境需要 Python 3.12+、Go 1.26.10、Node.js 24 和 npm。公开 Git 不包含原始上游快照；首次克隆后先补齐固定版本：

```powershell
git clone https://github.com/EmrysCheng/CLIProxyAPIPlus.git
cd CLIProxyAPIPlus
python scripts/hydrate_upstreams.py
python scripts/project.py prepare
python scripts/project.py build
```

产物在 `dist/<系统>-<架构>-portable/`，压缩包也在 `dist/`。构建会校验快照、应用补丁、构建真实前端、运行产品测试，并生成二进制、构建身份、许可资料及匹配源码配方。完整测试矩阵、Docker 和上游候选流程见[开发指南](docs/DEVELOPMENT_CN.md)。

## 当前边界与安全

- 当前支持文件认证存储及本地 SQLite；Home、PG/Git/对象 token store、CLI/TUI、mDNS、pprof 未在整合入口提供。
- Portable 禁用动态插件；Linux Full/CGO 和插件能力需单独原生验收，不能用交叉编译代替。
- 配置热更新不允许修改监听器、TLS、认证目录、内部管理密钥或开启额外监听器。
- 源码更新会执行经管理员确认的上游构建代码。隔离目录和摘要校验**不是 OS 安全沙箱**；请使用专用最小权限账户。
- 不公开 `config.yaml`、`data/`、密钥、备份或 `<data-dir>.updates/`；后者可能包含回滚数据和后续更新所需的活动配方。
- 普通验证 / 上游候选 CI 仍只导出白名单元数据。Portable 仅由手动发行流程在同提交验证通过、三个原生平台包齐全并核验后公开；不上传运行数据或原始日志。工程验证与安全、许可、具体生产环境签收分别记录。

## 文档与反馈

| 文档 | 内容 |
| --- | --- |
| [详细使用指南](docs/USER_GUIDE_CN.md) | 安装、登录、配置、API、模型价格、日志、备份、升级及排障 |
| [双上游源码自更新](docs/SOURCE_UPDATES_CN.md) | 页面检查、构建工具、网络、升级、回滚与信任边界 |
| [运维与数据迁移](docs/OPERATIONS_CN.md) | 旧部署导入、离线备份恢复、整包维护与故障恢复 |
| [开发与构建](docs/DEVELOPMENT_CN.md) | 目录结构、构建测试、上游候选、容器与贡献约束 |
| [实施状态](docs/IMPLEMENTATION_STATUS_CN.md) / [发行检查清单](docs/RELEASE_CHECKLIST_CN.md) | 已实现范围、历史证据及尚未签收的门禁 |
| [全部文档](docs/README.md) | 按使用场景选择入口 |

提交问题时请提供整合版本、两上游提交、系统架构、失败阶段及脱敏日志，不要上传密钥、认证文件、完整数据库或完整更新事务目录。

## 致谢与许可

感谢 CLIProxyAPI 与 CPA-Manager-Plus。两个上游的 MIT 许可及依赖声明按原文保留，详见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。上游快照可按锁定提交补齐；完整构建产物附带 `licenses/`、许可索引与合规报告。

上游 MIT 许可不等于本项目新增产品代码已获相同授权；新增代码的许可证尚待所有者明确。SBOM、校验和或成功构建均不代表法律、安全或生产发布审批已经完成。
