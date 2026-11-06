# CLIProxyAPIPlus 详细使用指南

适用版本：**`0.4.1` 正式版**。本文面向安装、配置和维护独立 Portable 实例的使用者；尚未完成全部生产验收。源码开发看[开发指南](DEVELOPMENT_CN.md)，验收范围看[发行检查清单](RELEASE_CHECKLIST_CN.md)。

## 目录

1. [选择部署方式](#1-选择部署方式)
2. [安装与首次启动](#2-安装与首次启动)
3. [登录与凭据](#3-登录与凭据)
4. [配置与日常页面](#4-配置与日常页面)
5. [接入模型客户端](#5-接入模型客户端)
6. [用量与预计花费](#6-用量与预计花费)
7. [日志配置](#7-日志配置)
8. [页面升级与回滚](#8-页面升级与回滚)
9. [备份恢复与旧部署导入](#9-备份恢复与旧部署导入)
10. [远程访问与安全](#10-远程访问与安全)
11. [常见问题](#11-常见问题)
12. [日常检查清单](#12-日常检查清单)

## 1. 选择部署方式

| 方式 | 用途 | 更新方式 |
| --- | --- | --- |
| 独立 Portable | 本机或受控服务器上的单程序部署 | 可显式开启页面双上游源码更新及回滚 |
| Windows Service / systemd 等托管部署 | 由外部进程管理器负责生命周期 | 使用受控停机维护；不要开启页面自动替换/重启 |
| Docker / Compose | 单程序容器，由容器平台负责替换 | 重建镜像、备份数据并由部署系统更新；不使用页面安装器 |

构建脚本提供 Windows amd64、Linux amd64、Linux arm64 目标；并非每个平台和模式都已经完成生产签收。Portable 不支持动态插件；Linux Full/CGO 需要另外的原生构建、插件及运行验证。Linux 包内 SDK 需要 glibc 2.28+，不是 Alpine/musl 工具包。

完整包运行常规业务不依赖系统安装的 Python/Go/Node。页面源码更新会使用包内 SDK，并启动临时编译、测试和安装进程；不是额外常驻业务服务。构建与测试可能消耗较多 CPU、内存及磁盘，按实际环境预留容量，不承诺固定耗时或任意配置都能完成。

## 2. 安装与首次启动

### 2.1 准备文件

从可信渠道取得经过核验的完整安装包，或自行按[开发指南](DEVELOPMENT_CN.md)构建。不要把原版 CLIProxyAPI EXE、Manager EXE 或单独 HTML 当成本项目安装包。从 [v0.4.1 发行页](https://github.com/EmrysCheng/CLIProxyAPIPlus/releases/tag/v0.4.1) 下载 `windows_amd64_portable.zip`、`linux_amd64_portable.tar.gz` 或 `linux_arm64_portable.tar.gz`。同时下载对应 `.sha256` 并核对；平台选择及校验命令见[首页](../README.md#1-准备完整-portable-目录)。不能把 GitHub 自动生成的源码 ZIP 直接当安装包运行；不支持的系统/架构不能混用程序包。

典型解压目录：

```text
CLIProxyAPIPlus/
  cliproxyapi-plus.exe          # Linux 为 cliproxyapi-plus
  build-info.json              # 产品版本、平台、源码摘要、上游身份
  checksums.txt                # 主程序 SHA256
  build-tools/                 # 包内 Python / Go / Node / npm 及许可
  update-tools.json            # 实际工具及构建网络配置
  update-tools.example.json    # 高级示例，不要覆盖实际配置
  updater-source/              # 与主程序摘要匹配的源码配方
  tools/                      # 离线维护脚本
  docs/                       # 随包运维与更新文档
  licenses/                   # 上游和依赖许可资料
```

安装后还会产生配置、数据及更新记录：

```text
  config.yaml                 # 首次 --init 生成
  data/                       # 本文显式指定的数据目录
    admin.key / api.key / core-management.key
    data.key / usage.sqlite   # Manager 启动时创建/使用
    auths/                    # 初始化配置使用的认证目录
  data.updates/               # 页面维护时产生，可能含活动配方和回滚快照
```

`checksums.txt` 记录的是主程序，不是“所有文件均已独立验证”的证明。可用以下命令与可信发布方给出的摘要核对：

```powershell
Get-FileHash .\cliproxyapi-plus.exe -Algorithm SHA256
Get-Content .\checksums.txt
```

Linux 使用 `sha256sum cliproxyapi-plus`。同包校验和只能发现不一致，不能单独证明发行者身份；来源、签名或可信渠道应另行确认。

### 2.2 全新安装

所有命令均在解压目录执行。示例显式使用 `data`；程序未传 `--data-dir` 时默认是 `.data`，不要混用两套目录。

Windows PowerShell：

```powershell
.\cliproxyapi-plus.exe --init --config config.yaml --data-dir data --listen 127.0.0.1:8317
.\cliproxyapi-plus.exe --config config.yaml --data-dir data --web-updates --source-updates
```

Linux：

```sh
./cliproxyapi-plus --init --config config.yaml --data-dir data --listen 127.0.0.1:8317
./cliproxyapi-plus --config config.yaml --data-dir data --web-updates --source-updates
```

`--init` 只创建新配置和新凭据，不启动业务服务；目标配置和数据目录必须尚不存在。第二条命令启动服务，并显式允许管理员通过页面执行源码更新与维护重启。

只需要业务运行时，第二条命令去掉 `--web-updates --source-updates`。这不影响手动离线维护；版本检查提醒与执行更新是不同权限，不会因为发现新版本就自动安装。

### 2.3 检查是否启动成功

```powershell
.\cliproxyapi-plus.exe --version
Invoke-RestMethod http://127.0.0.1:8317/plus-api/ready
Invoke-RestMethod http://127.0.0.1:8317/plus-api/version
```

就绪接口应返回 `ready: true`；版本接口可用于确认实际运行的产品和双上游身份。仅有进程存在或浏览器缓存页面，并不能证明新服务已就绪。

打开 `http://127.0.0.1:8317/`，兼容入口为 `/management.html`。若端口占用，先确认是否已有实例；不要对同一 SQLite/认证目录启动第二个程序。

### 2.4 已有实例换版

1. 记录原程序版本、配置/data 的绝对路径、启动参数、SDK 配置及活动更新记录位置。
2. 停止所有业务和维护写入进程，制作独立外部备份，保留旧完整安装目录。
3. 将经过验证的新**完整匹配包**解压到新安装目录；不要只换 EXE，也不要将新旧 `updater-source` 混合。
4. 使用原配置/data 路径和受控启动参数启动，核对版本、就绪、登录、认证及用量。
5. 需要撤回时恢复匹配的旧程序和升级前数据快照，不能让旧程序直接打开已迁移的新库。

已经通过页面进行过源码更新时，活动配方可能在 `data.updates` 内。不要为了“清理旧版本”删除该目录。跨产品版本的配方兼容和 schema 迁移应在副本演练，不能保证任意历史版本直接跨级升级。

## 3. 登录与凭据

| 文件/配置 | 用途 | 注意事项 |
| --- | --- | --- |
| `data/admin.key` | 首次管理员登录提示 | 日后修改管理员凭据以 Manager 数据库为准，修改文件不会自动改在线密码 |
| `data/api.key` | 初始化时提供的首个模型客户端 key 提示 | 后续有效 key 以 `config.yaml` 的 `api-keys` 等原生规则为准 |
| `data/core-management.key` | 程序内部核心管理调用 | 不填进浏览器、模型客户端或其他 CPA 连接 |
| `data/data.key` | Manager 加密材料 | 必须与数据库一起保留和备份，丢失可能无法解密旧数据 |

在可信本机读取 `admin.key`，将内容填入页面管理员凭据输入框；不要把密钥输出、截图或发到 Issue。整合模式的管理连接走本程序，不需要再启动原 Manager 或配置第二个 CPA 地址。

忘记管理员凭据时先确认是否已在 Manager 改密、是否启动了正确 data。确需重设，应停止服务、备份后由部署管理员使用离线 `--reset-admin --data-dir <实际目录>`；该操作从同目录 `admin.key` 读取目标凭据并修改验证器，不是关闭鉴权。不要删除 SQLite 或绕过管理员保护来恢复登录。

## 4. 配置与日常页面

### 4.1 配置职责

- **`config.yaml`**：核心原生 YAML，供应商、模型映射、客户端 key、认证目录、日志和监听配置。
- **Manager SQLite**：用量、仪表盘、任务、自动化及受保护连接设置。
- **`update-tools.json`**：源码更新所用工具和构建网络，由本机启动操作者维护，不从网页上传路径或命令。

初始化生成的配置包含隔离内部管理所需设置。不要从原版示例整份覆盖，不要改内部 `remote-management.secret-key`，不要把独立 CPA 的管理入口重新公开。提供商相关字段以当前锁定核心及页面支持为准。

### 4.2 建议使用顺序

| 页面 | 操作 |
| --- | --- |
| 配置管理 | 设置客户端 key 和核心配置；先备份再做大规模变更 |
| AI 提供商 | 配置实际供应商地址、凭据及模型映射 |
| 账户 / OAuth | 导入受支持认证材料，或执行经授权的供应商登录 |
| 仪表盘 / 用量分析 | 核对请求、Token、错误及统计时区 |
| 模型价格 | 同步/维护价格、确认匹配及别名，供费用估算 |
| 日志 | 查看运行错误，结合本机轮转文件排查 |
| 系统信息 → 程序更新 | 查看整合及双上游身份，检查、确认升级或回滚 |

页面能力受构建模式、供应商配置及实际认证材料影响。初始化不会自动获得供应商额度或保证 OAuth 联调成功。

### 4.3 哪些变更需要停机

监听 IP/端口、TLS、`auth-dir`、内部管理密钥、插件开启状态等在本次运行固定。页面和文件 watcher 均校验这些边界，不能通过编辑文件绕过。

需调整时：先备份、停止服务，离线修改并核对路径/权限，再按新配置受控重启。其他可热更新字段仍应先在副本验证，不承诺任何 YAML 修改都可不停机应用。

## 5. 接入模型客户端

OpenAI 兼容客户端的常见配置：

| 项目 | 示例 |
| --- | --- |
| Base URL | `http://127.0.0.1:8317/v1` |
| API key | 配置中有效的客户端 key，而不是管理员 key |
| 模型 | 当前供应商账户实际可用且已映射的模型 ID |

有些客户端会自行补 `/v1`，此时使用服务根地址，避免 `/v1/v1`。Gemini 等协议使用 `/v1beta/...` 的原生路径和鉴权约定；并非所有客户端都接受同一 Base URL 格式。

本机可在不打印 key 的情况下列出模型：

```powershell
$apiKey = (Get-Content -Raw .\data\api.key).Trim()
Invoke-RestMethod -Uri 'http://127.0.0.1:8317/v1/models' -Headers @{ Authorization = "Bearer $apiKey" }
Remove-Variable apiKey
```

若已旋转客户端 key，改用安全保存的当前有效 key，不要误以为旧提示文件会自动同步。模型列表为空时先完成供应商与账户配置；不应通过关闭鉴权“解决”。实际生成请求可能产生供应商费用。

## 6. 用量与预计花费

**请求和 Token 已有数据，但“预计花费 $0.00”不一定是采集故障。** 费用估算还依赖价格表、模型匹配及输入/输出/缓存 Token 计价规则。

排查步骤：

1. 确认用量统计已启用，选择的统计时间范围与时区正确，并且确有请求。
2. 打开 `http://127.0.0.1:8317/#/model-prices`。前端使用 hash 路由，不是服务端 `/model-prices` 路径。
3. 检查价格表是否为空；可使用页面同步功能，再人工审阅候选价格。
4. 对照用量记录里的实际模型名确认精确匹配、别名及供应商前缀，处理未匹配项目。
5. 按真实供应商价格和页面标明的单位填写输入、输出及可适用的缓存费率，不复制无依据的价格。
6. 保存后等待后台重算、刷新统计；仍为零时核对未计价记录、实际 Token 明细及日志。

不要把“未配置价格”的零值当免费，也不要把估算当供应商实际账单。促销、缓存、套餐和供应商结算规则可能使两者不同。

## 7. 日志配置

### 7.1 默认文件日志

新安装默认配置：

```yaml
logging-to-file: true
logs-max-total-size-mb: 100
```

核心访问日志与 Manager 后台日志写入共享 `main.log`；单文件约 10 MB 轮转。上游每分钟按 100 MB 的总量目标清理旧日志，活跃文件受保护；这不是瞬时硬配额。旧配置未指定字段时使用产品默认值，显式 `false` / `0` 仍保留其含义。

日志目录选择顺序：

1. `WRITABLE_PATH/logs`；
2. 工作目录中已存在且可写的 `logs/`；
3. 否则回退到原生 `auth-dir/logs`。

**以启动输出的 `Application log: <绝对路径>` 为准。** 本文默认认证目录布局下可能为 `data/auths/logs/main.log`，不要硬编码此假设。日志文件无法打开会启动失败，不会静默丢弃；运行中磁盘耗尽仍需容量监控。

### 7.2 固定日志基目录

在应用启动前配置，不是新增 CLI 参数：

```powershell
$env:WRITABLE_PATH = Join-Path $PWD 'runtime'
New-Item -ItemType Directory -Force -Path $env:WRITABLE_PATH | Out-Null
.\cliproxyapi-plus.exe --config config.yaml --data-dir data --web-updates --source-updates
```

Linux 可用 `mkdir -p runtime` 后以 `WRITABLE_PATH="$PWD/runtime" ./cliproxyapi-plus --config config.yaml --data-dir data` 启动。目录应仅允许部署账户写入；不要放入无关 `.log` 文件，以免被上游清理器识别。无需自行添加 `--log-dir`，程序没有此参数。

### 7.3 更新日志

页面维护记录位于 `<data-dir>.updates/<operation-id>/`：

- `source-build.log`：源码构建输出，最多记录 64 MiB。
- `worker.log`：维护辅助进程和构建前预检错误。
- `application-console.log`：更新后后台业务实例的启动控制台输出；常规运行仍使用 `main.log`。

页面仅展示脱敏的阶段和错误分类。本机原始输出可能包含路径或敏感上下文，分享前逐项脱敏；不要上传整个维护目录。

## 8. 页面升级与回滚

### 8.1 开启默认模式

```powershell
.\cliproxyapi-plus.exe --config config.yaml --data-dir data --web-updates --source-updates
```

`--web-updates` 允许安装、维护重启及回滚；`--source-updates` 额外授权执行固定上游的构建代码。两者共同开启才允许源码安装。不传 `--release-index`，也不需要启动 26973 等发行服务；如从历史模式切换，也清理启动环境中的 `CLIPROXY_PLUS_RELEASE_INDEX`。

仅支持满足检查条件的独立 Portable 实例：固定监听 IP/端口、私有可写安装及数据目录、自包含认证/TLS 材料等。页面显示禁用原因时应处理部署问题，不要绕过保护；托管进程/容器请由部署系统换版。

### 8.2 工具与网络

保留默认实际 `update-tools.json`：

```json
{
  "bundle": "./build-tools",
  "go_proxy": "https://goproxy.cn|https://proxy.golang.org"
}
```

- 路径相对 JSON 所在目录解析，默认使用安装程序旁的配置和工具，不依赖系统 PATH。
- 工具文件、权限、版本及清单在执行前检查；缺失或修改会阻止构建，不悄悄使用系统旧 SDK。
- `go_proxy` 非空优先于环境 `GOPROXY`；想继承环境就先删除该字段或设为空字符串，再在启动终端设置环境并重启。
- 包内 `GOENV=off`、`GOTOOLCHAIN=local`，宿主 `go env -w` 不生效，也不会暗中下载另一版 Go。
- `npm_registry` 默认未配置；按需添加可信 HTTPS registry。代理地址不得内嵌凭据或查询参数，不关闭 TLS、Go checksum 或 npm 锁文件校验。
- 内置 SDK 免去下载工具，但 GitHub 元数据、上游源码、Go/npm 依赖仍需联网。`|` 使 Go 代理在任意错误时尝试下一项，`,` 仅按规定响应回退。

修改本机工具配置后，页面重新检查并手动确认新的构建，不会自动重试上次失败。高级自定义路径、令牌及信任边界见[源码自更新文档](SOURCE_UPDATES_CN.md)。

### 8.3 执行升级

1. 制作独立备份，确认磁盘、网络和业务停机窗口，使用管理员凭据登录。
2. 进入“系统信息 → 程序更新”（`/#/system/updates`），点击“检查上游更新”。后台也会启动检查并按周期检查，但不会自动安装。
3. 分别核对 CLIProxyAPI 与 CPA-Manager-Plus 的当前及目标版本/提交。检查失败或陈旧不等于已经最新。
4. 选择单侧更新或“一起更新两个项目”，输入页面展示的确认文本，明确确认信任目标源码执行。
5. 在“升级进度与诊断”查看阶段、耗时及最近真实活动。17 个构建检查阶段的完成数不是下载或耗时百分比，也不预测剩余时间。
6. 构建和必要验证失败不会安装，旧业务进程继续工作；按失败阶段查本机日志后再手动重试。
7. 验证通过后进入排空、快照、替换、迁移及健康检查，会短暂停机。页面断线时等待重连，不重复提交或启动第二实例。
8. 成功后核对两个上游的版本/提交、就绪状态、供应商、用量和模型请求。整合版本号可能不变，以双上游身份及成功维护记录判断。

这里只更新被确认的基础项目组合；配方中的整合代码不会自动变成本产品仓库未来版本。需要新的整合功能或 SDK 时，安装经过验证的新完整产品包。

### 8.4 回滚与中断恢复

只有与当前程序匹配的成功维护记录和快照才可回滚，不能任填旧上游标签。页面确认旧版本并勾选数据快照恢复影响后，恢复旧程序、双上游组合、配置、数据及原配方引用。

**回滚不合并快照后的用量、配置或业务变更。** 现有数据另行保留供人工核查；重要数据仍应事先做独立备份。安装/迁移失败会尝试成对恢复；不能安全确认时停在 `recovery_required` 并保留维护标记。

遇到断电、辅助进程崩溃或恢复失败，停止相关进程并保留整个目录，按[运维文档的离线恢复步骤](OPERATIONS_CN.md#81-私有记录与故障恢复)使用该操作保存的 worker 副本处理，不从正在替换的程序路径运行恢复，不直接删除 `.maintenance` 或锁文件。

## 9. 备份恢复与旧部署导入

### 9.1 一致性备份

停止业务实例及所有维护写入进程，确保没有其他程序写同一个数据库或认证目录。备份目标必须位于 data 外且尚不存在。以下示例从完整 Windows 包目录使用包内 Python：

```powershell
.\build-tools\python\python.exe -B .\tools\data.py backup --binary .\cliproxyapi-plus.exe --config .\config.yaml --data-dir .\data --snapshot .\backups\before-update.zip
.\build-tools\python\python.exe -B .\tools\data.py verify --snapshot .\backups\before-update.zip
```

换一个新快照名执行下次备份。Linux 使用 `./build-tools/python/bin/python3`、`./cliproxyapi-plus`；源码工作区中将 `tools/data.py` 改为 `scripts/data.py` 并显式指定实际 binary。

工具利用跨进程锁、SQLite backup API 和完整性检查，覆盖配置、数据库、加密材料、凭据、认证、任务归档及启用的 TLS 材料。**ZIP 含秘密，且不等于加密备份。** 移动到其他磁盘或存储后核对访问权限并使用受控加密存储；不要公开上传。

### 9.2 恢复到新路径

使用与快照兼容的可信程序，停止相关进程；目标配置与 data 必须均不存在：

```powershell
.\build-tools\python\python.exe -B .\tools\data.py restore --binary .\cliproxyapi-plus.exe --snapshot .\backups\before-update.zip --config .\restored\config.yaml --data-dir .\restored\data
.\cliproxyapi-plus.exe --check-data --data-dir .\restored\data
.\cliproxyapi-plus.exe --config .\restored\config.yaml --data-dir .\restored\data
```

恢复会重定位认证/TLS 路径，不覆盖现有部署；最后一条是人工确认后的业务启动示例。恢复失败留下部分目标时，不启动半成品、不强行覆盖重试，保留证据并使用另一个新路径。

### 9.3 从两个原始服务导入

先停止源 CPA 和源 Manager；源路径只读，目标安装目录必须尚不存在。支持的 Manager 源布局为本地 `usage.sqlite` 与 `data.key`；历史不兼容版本先在副本走官方迁移路径。

在源码工作区执行并替换所有尖括号占位：

```powershell
python scripts/data.py import --binary <整合程序绝对路径> --native-config <旧核心配置> --auth-dir <旧认证目录> --manager-data-dir <旧Manager数据目录> --destination <尚不存在的新安装目录> --listen 127.0.0.1:8317
```

工具复制数据并生成目标管理员/内部管理凭据，不会修改源。迁移后核对新管理员、客户端 key、提供商、认证、任务及历史用量；完整参数与外置 TLS 注意事项见[运维与数据迁移](OPERATIONS_CN.md)。

## 10. 远程访问与安全

本机默认 `127.0.0.1:8317`，其他机器不能直接访问。确需局域网访问时，在受控停机后离线设置绑定地址，并显式授权，例如：

```powershell
.\cliproxyapi-plus.exe --config config.yaml --data-dir data --listen 192.168.1.10:8317 --allow-remote
```

示例 IP 必须是本机实际地址；启动前配置 TLS/可信访问通道、限制防火墙来源并确认管理员凭据强度。`--allow-remote` 只解除业务监听保护，不是开启全部核心管理接口，也不是关闭 Manager 鉴权。

- 不将管理端口直接以明文暴露公网；浏览器远程访问的 URL 应改为部署的受保护入口。
- 安装、数据、SDK、配方及其父目录只允许可信部署账户写入；源码构建权限等于服务账户权限，不是 OS 沙箱。
- 不把配置、认证、数据库、密钥或 `.updates` 加入 Git；不把它们暴露为静态目录。
- OAuth 如需回环回调，要按供应商及部署环境验证；单业务端口不意味着 OAuth 全流程不使用临时回调监听。
- API key 与管理员凭据分开管理；轮转后检查当前配置/验证器，不依赖旧提示文件。

## 11. 常见问题

| 现象 | 先检查什么 | 不应采取的做法 |
| --- | --- | --- |
| 管理页 401/403、登录失败 | 当前 data、Manager 是否已改密、是否误用客户端/内部 key | 关闭鉴权、删数据库、公开内部核心 key |
| 预计花费为零 | 价格表、模型别名、时间范围及后台重算 | 杜撰费率或把零值当免费 |
| 页面升级按钮禁用 | 两个更新开关、独立 Portable 条件、工具与配方身份、页面禁用原因 | 绕过后端限制或仅换 EXE |
| `release index must be an absolute HTTPS URL without credentials` | 默认模式移除 `--release-index` 和旧环境变量；确需索引时看兼容文档 | 给默认源码更新额外搭发行服务 |
| GitHub 检查失败 | 实际网络、API 限流、页面检查时间及错误 | 当成已是最新或自动安装陈旧目标 |
| Go/npm 下载超时 | 实际 `update-tools.json`、网络代理和依赖服务；`go_proxy` 优先级 | 关闭 TLS/校验、只运行宿主 `go env -w` |
| SDK/配方校验失败 | 完整同平台包、文件是否改动、程序与配方/lock 是否匹配 | 使用来源不明的工具或手改摘要 |
| 补丁、编译或测试失败 | 失败阶段、构建日志、上游兼容性 | 跳过测试强装；未来上游可能需要新适配 |
| 更新时页面断线 | 是否已进入维护重启、等待重连、查看私有记录 | 连续重复升级、再启动第二实例 |
| `.maintenance` / `recovery_required` | 所有写入进程是否停止、对应记录及完整快照 | 盲删锁、清空历史、只回滚 EXE |
| 日志仍刷屏或找不到文件 | 是否显式关闭文件日志、实际启动配置和 `Application log:` | 假定日志总在安装根目录 |
| “升级成功”后整合版本没变 | 两个上游版本/提交与成功操作记录 | 仅凭整合版本号判断失败 |

提交问题前收集：`--version`、系统与架构、启动模式、失败阶段、脱敏错误及发生时间。不要提交密钥、完整环境变量、认证 JSON、数据库、备份或完整更新目录。

## 12. 日常检查清单

- [ ] 核对实际运行版本、双上游提交和 `ready`，不要只看缓存页面。
- [ ] 新供应商/认证配置后验证模型列表与一次经授权的调用。
- [ ] 确认用量采集、时区、模型价格和未匹配模型处理情况。
- [ ] 定期检查 `main.log`、更新目录和磁盘空间；不要自动删除活动配方/回滚记录。
- [ ] 升级前做外部备份，在副本演练恢复，并明确快照后数据的回滚影响。
- [ ] 保留最小权限、访问边界和秘密管理；页面检查不等于自动安装许可。
- [ ] 正式投产前逐项完成[发行检查清单](RELEASE_CHECKLIST_CN.md)，不以版本通道或文档齐全替代生产验收。
