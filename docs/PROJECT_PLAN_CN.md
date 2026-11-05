> 2026-11-04 构建工具交付调整：Python、Go、Node、npm 随目标平台安装包分发，用户无需下载 SDK；默认校验并使用包内工具。打包端按固定归档 SHA256 获取工具，源码更新复用本地内置工具，仍须联网获取上游源码与项目依赖，不额外启动发行服务。

> 2026-11-04 用户需求调整（已授权实施）：默认更新改为页面直接检查两个固定上游正式版，在操作者显式授权后以本机构建工具在私有隔离目录完成源码适配/测试/构建，再由原维护引擎安装和成对回滚；不再要求额外发行服务或索引。原计划中“不在运行环境编译”的限制被此明确需求修订，其他单端口、管理员鉴权、快照一致性、上游只读和生产门禁保持不变。现行操作方式见 SOURCE_UPDATES_CN.md。

# CLIProxyAPIPlus 项目合并计划（已批准）

- **状态：2026-11-03 已获用户批准，已进入实施；实际进度见 IMPLEMENTATION_STATUS_CN.md。**
- 编制日期：2026-11-03（Asia/Shanghai）。
- 上游版本最后核验：2026-11-03 22:46（UTC+08:00），GitHub API 只读核验。
- 当前工作区：`D:\Code\Project\AI\CLIProxyAPIPlus`，调研时为空目录，尚未初始化 Git 仓库。
- 本轮产物仅为本计划；上游调研快照下载在系统临时目录，不作为产品代码提交。

## 1. 目标与需求解释

将以下两个项目在**源码与构建层面集成**成新的 CLIProxyAPIPlus 程序：

1. `router-for-me/CLIProxyAPI`：负责模型代理、协议适配、认证文件、供应商接入与相关核心能力。
2. `seakee/CPA-Manager-Plus`：提供唯一的管理前端，并复用其管理后端、用量持久化、仪表盘与自动化能力。

推荐交付形态：**一个业务进程、一个对外业务端口、一个可执行程序，前端资源随程序内嵌**。Docker 镜像运行的也应是这个新程序，而不是由容器同时启动两个原始服务。

“使用同一个端口访问两个服务”按以下体验设计，端口示例为 8317：

| 使用场景 | 目标入口 |
| --- | --- |
| 打开管理前端 | `http://localhost:8317/`，跳转到或展示 CPA-Manager-Plus 页面 |
| 兼容原页面入口 | `http://localhost:8317/management.html` |
| OpenAI 兼容调用 | `http://localhost:8317/v1/...` |
| Gemini 兼容调用 | `http://localhost:8317/v1beta/...` |
| 管理与用量接口 | 同域同端口下的原有管理路径 |

“支持持续更新最新代码”拆成两个能力：

- **源码同步与重构建**：能够独立或同时同步两个指定上游，按确定的提交重新构建新程序，并通过兼容性检查。
- **产品升级**：用户安装的是本项目验证后的整包发行物，而不是分别更新其中一个后端或一份网页。

**不把“持续更新”解释为生产环境不经测试就自动追随 main。** 支持跟踪 main 的最新源码，但发布必须经过验证与审批。

## 2. 已核验的上游基线

| 项目 | 最新正式版 | 正式版对应完整提交 | 默认分支 | 发布日期（北京时间） |
| --- | --- | --- | --- | --- |
| CLIProxyAPI | `v8.0.53` | `01b24f1f95dc9b309765212c52b25b25e005ff01` | `main` | 2026-11-03 20:27:20 |
| CPA-Manager-Plus | `v1.14.10` | `a67ed8834a914ab24c36d2490fa6bb6d4166724c` | `main` | 2026-10-29 12:45:36 |

核验时，两仓库 main 的 HEAD 分别与上述正式版提交相同。正式实施时必须重新检查最新版本；不能把本次调研基线长期视作“最新”。

### 2.1 与合并直接相关的代码事实

1. **后端技术栈可统一。** 两个基线后端均使用 Go，`go.mod` 均声明 Go 1.26.10；CPA-Manager-Plus 前端为 React、TypeScript、Vite。
2. **CLIProxyAPI 已有嵌入式 SDK。** `sdk/cliproxy` 提供 Builder 与运行生命周期；`sdk/api/options.go` 提供 `WithEngineConfigurator`、`WithRouterConfigurator` 等公开扩展点。
3. **管理端不是一份纯静态网页。** Manager Server 提供 `http.Handler`，但数据锁、SQLite 初始化、密钥加载、采集与后台任务启动/停止仍大量位于原 `cmd/cpa-manager-plus/main.go`。只调用 HTTP handler 不足以保留完整功能。
4. **两个项目的管理路由重叠。** Manager 自身处理部分 `/v0/management/*`，其余管理请求再交给 CPA；直接把两个路由器平铺注册会引入冲突，或者绕过 Manager 的凭据、认证文件修改保护。
5. **v8 兼容边界需要保留。** 当前 Manager 对 `/v8/management/config/api-keys` 有专门兼容和管理员验证，并未开放任意 v8 管理子树。不能简单将所有 `/v8/management/*` 裸露到核心。
6. **用量采集存在 HTTP 路径。** Manager 支持 HTTP、RESP 等采集模式；HTTP 采集请求 `/v0/management/usage-queue`，适合初期通过进程内传输复用，避免引入另一套用量计算规则。
7. **核心监听器不只是普通 HTTP。** 当前核心在同一监听端口复用 HTTP 与 RESP。集成时优先保留其监听器和协议处理，避免为了统一 HTTP 而破坏原有能力。
8. **前端需要重新编译后内嵌。** Manager 的构建流程将 Web 构建结果复制到 `internal/httpapi/web` 后进行 Go 构建；源码中的占位 HTML 不是可交付页面。
9. **构建不能盲目统一成 CGO=0。** 当前核心的 Unix 动态插件支持涉及 CGO，必须按平台声明插件能力；不能因为 Manager 可用纯 Go 构建，就宣称合并后所有功能都可静态跨平台编译。
10. 两个上游均采用 MIT License；源码与发行包需要保留各自许可和版权信息，并核对依赖的第三方声明。

上述事实来自本次锁定提交的源码检查。源码路径与官方资料索引见第 12 节。

## 3. 合并策略与总体架构

### 3.1 推荐策略

**保留两个上游的独立源码边界，以薄适配层完成同进程集成；不做两个仓库文件的扁平混合，也不维护大面积业务逻辑分叉。**

```text
浏览器 / API 客户端
           |
       同一业务端口
           |
CLIProxyAPI 原生监听器 / TLS / 协议复用
           |
       统一 HTTP 分发层
           |
           +-- 数据面 --> CLIProxyAPI 原始处理链
           |              模型、流式响应、WebSocket、供应商协议
           |
           +-- 管理面 --> CPA-Manager-Plus Handler
           |              前端、管理员校验、SQLite、自动化
           |                        |
           |                 进程内 CPA Transport
           |                        |
           +----------------> 核心管理处理链（仍执行核心鉴权）
```

运行时不依赖另一份 CPA 服务、不启动 Manager 的第二个业务监听器、不依赖 Nginx 来实现合并。

### 3.2 两侧的集成方式

**核心侧：**

- 优先通过公开 SDK 和引擎配置钩子注入统一分发，不重写协议转换器、执行器和认证管理器。
- 保留核心原生 HTTP/RESP 监听器、TLS、请求体处理与数据面中间件。
- 通过最小必要的公开桥接包补齐 SDK 未涵盖的启动职责；不能从产品模块直接导入上游 `internal` 包。
- 对原命令行入口中的配置、token store、模型目录更新、插件、日志、版本信息与可选存储初始化逐项做覆盖清单，不能假设直接调用 Builder 就等于原程序全部能力。

**管理侧：**

- 在 Manager 自己的 Go module 内增加一个公开的嵌入式运行包，例如 `pkg/embedded`，由它合法复用本模块的 `internal`。
- 从原 main 提取完整的初始化和清理生命周期，暴露 handler、就绪状态、后台任务启动与有界停止；不只包装 `httpapi.New`。
- 原 Manager 独立入口继续可用，并复用同一运行包，尽可能防止嵌入模式和独立模式发生功能漂移。
- 嵌入模式不再启动自己的业务端口；数据锁、加密密钥、迁移、采集器、自动化、归档、维护任务必须完整保留。
- 所有初始化错误返回给统一入口；桥接代码不调用 `os.Exit` 或 `log.Fatal`。

### 3.3 进程内调用，不再连接第二个后端

Manager 访问内嵌 CPA 使用**显式注入的 HTTP Client / RoundTripper**：

- 对内部虚拟目标执行进程内调用，而不是请求另一个 localhost 监听端口。
- 核心管理请求仍携带独立内部管理凭据，并通过原核心校验。
- 内部调用使用私有 Go context 标记跳过“再次交给 Manager”的分发，避免自代理死循环。该标记不能通过请求头、URL 参数或伪装回环地址触发。
- Transport 必须保留状态码、响应头、取消、超时和流式读取语义；不得通过全量缓存响应来图省事。
- 必须覆盖：管理反代、连接校验、认证文件、认证快照、HTTP 采集、账户动作、巡检和后台任务涉及的 CPA 请求。
- 不全局替换 `http.DefaultClient` / `http.DefaultTransport`。模型价格、外部版本索引等正常外联使用单独客户端。
- 启动采用分阶段装配：准备密钥/数据锁/数据库，构建核心处理链，绑定内部 Transport，发布 Manager handler，再启动依赖两侧的任务并报告就绪；具体顺序通过 P0 解决原初始化依赖，不允许用重试掩盖循环依赖。
- 分发保护从首个请求开始生效。Manager 尚未就绪或初始化失败时，管理路径返回 503，不得退回未经 Manager 保护的核心管理入口；任一关键组件失败都应触发统一关闭。
- 内部目标、可用路由与重定向必须有限制；不能把浏览器传来的任意目标自动提升为内部高权限请求。

**P0 准入要求：**验证公开扩展点能否稳定挂载分发层、完整覆盖生命周期，并实现不递归的进程内管理调用。若必须大范围修改核心或产生不可接受的补丁，先提交方案修订，不自行退化为双进程反向代理。

## 4. 路由、鉴权与前端设计

### 4.1 路由所有权

以下为设计规则，P0 会以最新源码枚举方法、精确路径与优先级，形成可自动测试的路由清单。

| 路径/类别 | 所有者与规则 |
| --- | --- |
| `/`、`/management.html`、管理页图标 | Manager 前端；根地址展示或跳转管理页 |
| `/setup`、`/status`、`/usage-service/*` | Manager，保持原接口语义并校验初始化安全 |
| `/v0/management/*` | 先由 Manager 处理其自有接口和权限，再通过内部 Transport 调用核心；外部不能绕开此层 |
| `/v8/management/config/api-keys` 及受支持子路径 | Manager 的显式兼容规则，不扩大原有方法和权限范围 |
| 其他 `/v8/management/*` | 默认不开放；如确有需要，逐项补齐保护和测试后显式准入 |
| `/v0/resource/plugins/*` | 保留 Manager 对管理员凭据与普通调用者凭据的区分，不默认替换为高权限凭据 |
| `/v1/*`、`/v1beta/*`、其他原生数据面路径 | 核心原处理链；实际清单包括现有扩展协议路由，而不只 OpenAI 两个接口 |
| `/v1/models` | 数据面身份验证；不能把 Manager 管理员 key 自动当作推理 API key |
| OAuth 回调 | 由核心处理已核实的精确回调路径；包括必要的 v8 回调例外，并保留 state/nonce/时效校验 |
| `/health` | 兼容 Manager 原探针；不泄露敏感状态 |
| 产品就绪与版本接口 | 新增独立命名空间，避免抢占两边已有接口；返回产品版本与双上游基线，敏感诊断需鉴权 |

禁止以静态页面兜底未知 API；未知 API 应返回正确的 404/405，而不是 HTML 200。

### 4.2 三类凭据分离

1. 模型调用 API key：只用于核心数据面。
2. Manager 管理员凭据：用于页面和管理能力。
3. 内部 CPA 管理凭据：由服务端生成或安全配置，只供 Manager 内部调用，不能下发到前端或写入普通日志。

保留原权限能力及插件公开资源的调用者身份语义。测试伪造内部头、编码路径、点路径、尾斜杠、方法绕过与权限替换；不能把请求来自本机当作免鉴权依据。

本机运行默认建议绑定 `127.0.0.1:8317`；对外访问显式配置监听地址、TLS 和访问控制。首次初始化需要防止未授权抢占；生产默认关闭额外调试监听。

### 4.3 前端范围

- 使用 CPA-Manager-Plus 的现有 React 页面、布局、交互和主要功能，不重新设计一套控制台。
- 仅调整统一产品标识、版本展示、同源连接、嵌入模式的连接设置及产品更新入口。
- 嵌入模式默认连接当前程序；不要求用户再填写“另一个 CPA 地址”。任意远程 CPA 目标切换不作为首期目标。
- 将前端构建结果、favicon 等嵌入新 Go 程序；浏览器刷新、静态资源和缓存策略一并验证。
- 禁止核心旧控制台自动下载逻辑覆盖本项目内嵌页面；前端版本与后端版本必须按整包配套发布。
- 原核心版本头、能力检测字段仍反映真实核心能力，不用产品版本替换它们，否则会误导 Manager 的兼容判断。
- 页面中的“更新”指向本产品发行物；可展示两个上游有新版本，但不能引导用户单独替换原版 CPA 或 Manager 程序。

### 4.4 单端口边界

验收要求是**一个对外业务端口**，不另开永久 Manager 端口。OAuth 平台可能规定固定的本地回调 URI；优先复用主端口，若特定登录流程必须使用临时回环监听，需在 P0 列出具体供应商、端口与生命周期，作为评审例外，不默认开放到外网。不能在未核验前承诺所有第三方 OAuth 流程永远只创建一个 socket。

## 5. 用量、配置与数据兼容

### 5.1 用量链路

- 第一阶段复用核心已有 usage queue 与 Manager 的解析、去重、持久化、聚合和自动化逻辑。
- 嵌入模式优先显式使用 HTTP 采集模式，经内部 Transport 取队列，避免 `auto` 模式先尝试不适用的网络订阅。
- 不同时运行两条消费者链，不新建第二套统计口径；原 RESP 能力保持原有边界。
- 验证调用到页面展示的完整链路、模型与账户映射、失败事件、重试、重启和积压行为。
- 在证明上游队列语义前不承诺 exactly-once 或崩溃零丢失。明确“出队后、入库前崩溃”的可能窗口；若零丢失成为硬需求，需要额外设计持久化/确认机制并重新评审。
- 直接对接核心 usage plugin 的高性能路径属于后续优化，不与首期兼容接入同时推进。

### 5.2 配置边界

- 产品入口统一管理监听地址、两侧配置路径、数据目录和版本信息。
- 尽量保留核心原生 YAML、认证文件和 Manager 原有配置/SQLite 结构，不把两套配置粗暴转换成一个新 schema。
- 核心配置热更新和认证文件监控继续由原机制负责；管理页面保存配置后必须能够回读并实际生效。
- 嵌入模式的 CPA 连接是产品内部绑定，防止 UI 错误修改导致自代理循环或转向不可信目标。
- 密钥、认证文件、数据库、日志、归档均与安装目录解耦，升级不得覆盖用户数据。

### 5.3 迁移与回滚

- 支持导入/指定已有核心 YAML 与 auth 目录，以及 Manager 的配置、SQLite、归档和加密密钥文件。
- 已有 Manager 数据的解密 key 必须随数据迁移；不能发现缺 key 后悄悄新建并破坏已有连接密文。
- 检查源服务是否仍在运行、数据库进程锁、SQLite WAL/SHM，禁止直接复制运行中的 SQLite 主文件当作一致性备份。
- 首次迁移采用原生备份/快照或停机一致性复制，在副本上验证；源数据默认保留。
- 记录数据 schema 版本、迁移结果和旧程序路径；提供演练步骤。
- **程序回滚不等于数据回滚。** 若新版本发生不可逆 schema 迁移，回滚必须同时恢复匹配的数据快照。
- 明确备份恢复会丢失备份之后的新数据；恢复前保留失败版本的数据副本，不承诺无条件无损降级。

## 6. 双上游持续更新方案

### 6.1 源码管理：原始快照 + 锁定清单 + 可重放补丁

推荐使用本仓库内可追溯的上游源码快照，而不是把两个仓库历史直接做 unrelated merge。

- `upstream/cliproxyapi` 与 `upstream/cpa-manager-plus`：分别保存锁定提交的原始源码，保留原模块路径和许可证。
- `upstream.lock.yaml`：记录 URL、通道、tag/branch、完整 commit SHA、源码摘要、同步时间与兼容验证结果。
- `patches/cliproxyapi` 与 `patches/cpa-manager-plus`：只放必要的嵌入式桥接和配置/Transport 注入补丁，记录用途与适用基线。
- 构建阶段将快照物化到隔离构建目录，严格按顺序重放补丁；源码快照本身保持原样，不在其中手改再伪称原始上游。
- 产品 Go module 通过工作区或 replace 引用物化后的两个原模块；保留合法的 `internal` 边界。
- 每次补丁重放必须检查上下文、前后置条件、文件摘要或等价规则，不能自动采用模糊匹配。
- `go.mod`、`go.sum`、Node lockfile 与合并后的 Go 依赖选择均需审核；两个模块单独能编译，不代表合到同一依赖图就一定兼容。

相较 submodule，本方案使普通克隆和离线源码审查更直接；代价是仓库体积和同步 diff 较大。若 P0 发现体积不合适，再评审改为固定 submodule SHA，但不改变单程序源码集成目标。

### 6.1.1 公开仓库分发修订（2026-11-05）

按用户要求继续执行公开仓库 `master` 交付，并针对 GitHub 的上游 OAuth 常量保护阻断调整源码分发：Git 中仅保存整合代码、补丁与 `upstream.lock.json`；原始快照保留在本地不可变 `upstream/`，首次克隆/CI/Docker 通过 `scripts/hydrate_upstreams.py` 按官方仓库完整提交补齐并核对原树摘要。以上修订替代本节“完整快照直接入库”的初始建议，不采用秘密豁免、常量重编码或原地篡改。

包内源码配方、自更新协议、独立鉴权、单端口和构建 SDK 内置方案不变；已有有效快照只验证，已有损坏快照拒绝覆盖，离线缺失明确失败。Git 历史中不应包含待排除常量；尚未公开的首次导入须在本地保留恢复证据后形成干净根提交，不强推覆盖他人远程历史。此修订不签收第三方常量的可公开再分发性，也不解除正式发布门禁。

### 6.2 更新通道

| 通道 | 上游选择 | 推荐用途 |
| --- | --- | --- |
| stable，默认 | 各仓库最新非预发布正式 tag，解析到不可变 commit | 正式发布 |
| edge，可选 | 各仓库 main 的最新确定 SHA | 跟踪真正的最新源码，验证后产出预览包 |
| pinned | 用户指定的已验证 tag / SHA 组合 | 故障定位、长期部署和回滚 |

两个上游可以单独升级，也可以一起升级。产品版本独立管理，并携带“核心版本 + Manager 版本 + 双 SHA + 补丁版本 + 构建版本”。

### 6.3 同步与构建流水线

```text
手动触发 / CI 定时检查（建议每天）
    -> 查询两个官方上游，解析候选 SHA
    -> 同源校验、下载、生成变更报告
    -> 在候选分支整体更新锁定清单和源码快照
    -> 重放补丁，审查模块/依赖/路由/数据迁移差异
    -> 两侧原有测试 + 产品契约测试 + 前端检查
    -> 编译目标程序与镜像
    -> 端到端、迁移、升级回滚演练
    -> 创建候选发布 / 更新 PR
    -> 人工审批后发布产品整包
```

建议提供跨平台脚本入口，实现：检查、同步单侧/双侧、指定 ref、验证、构建和还原指定锁定版本。具体命令名在实施时确定，本计划不创建这些脚本。

更新失败必须可解释：补丁冲突、路由变更、v8 语义变化、数据库迁移、依赖冲突分别报告；不得吞错、强行升级或覆盖当前已验证基线。

### 6.4 CI 安全与运维边界

- 更新检测只读；不在带生产密钥的流水线上直接执行未评审上游脚本。
- 候选代码测试使用低权限、无发布凭据的隔离环境；发布凭据仅在审批后的发布作业使用。
- 构建失败不影响正在运行的版本；已验证版本与安装数据保持不变。
- 默认“自动发现 + 自动验证/构建 + 人工发布”，不默认自动覆盖生产服务。
- GitHub 不可用时允许根据本地快照、锁定清单和已缓存依赖重构建；缺少依赖时明确报错，不静默换版本。
- 首期不实现程序运行时自行执行 `git pull`、安装编译器、在线编译或零停机热替换。安装升级由整包/镜像和服务管理器完成。

## 7. 构建与发行

### 7.1 候选项目布局

```text
CLIProxyAPIPlus/
  cmd/cliproxyapi-plus/       # 唯一产品入口
  internal/integration/      # 路由、生命周期、内部 Transport、版本组合
  upstream/
    cliproxyapi/             # 上游原始快照
    cpa-manager-plus/        # 上游原始快照
  patches/                   # 可重放的薄适配补丁
  scripts/                   # 检查、同步、准备、测试、构建、打包
  tests/                     # 契约、E2E、升级回滚
  docs/                      # 计划、兼容矩阵、接入/运维/升级文档
  upstream.lock.yaml
  go.mod / go.sum
  Dockerfile
  .github/workflows/
```

这只是评审后的预期结构；本轮不创建其中的实现文件或工作流。

### 7.2 构建顺序

1. 检查锁定清单、源码摘要与补丁版本。
2. 在隔离构建目录准备两侧源码并重放补丁。
3. 使用 Manager 的 Node lockfile 安装构建依赖，执行前端检查和构建。
4. 将真实构建资源注入 Manager 的嵌入路径，验证不是源码占位页。
5. 用统一 Go 依赖图构建产品入口，注入真实核心、Manager 和产品版本信息。
6. 执行功能检查，生成包、校验和、SBOM、许可清单和版本清单。

### 7.3 平台与功能矩阵

首期建议验证：Windows amd64、Linux amd64、Linux arm64；Linux 同时提供新程序的 OCI/Docker 镜像。macOS 视实际部署需求作为后续扩展，不默认拉入首期。

- Node.js 和 Go 只在构建阶段需要，安装后不要求用户再安装 Node 或运行两份服务。
- Linux 涉及动态插件的完整构建要使用适配的 CGO 工具链和运行时，不能与纯 Go 无插件构建混为一谈。
- 纯 Go 便携包如有必要需明确标注功能裁剪；Windows 插件行为单独实测，不从 Linux 推断。
- P0 必须列出核心已有的插件、可选存储、控制平面、CLI/TUI 等能力，标注“首期支持/保留待验/明确不纳入”。任何相对原程序的缺失都需评审，不能默默删除。
- 具体系统最低版本、CPU 架构和镜像基础系统在 P0 确认，不在调研阶段承诺未经构建验证的兼容性。

## 8. 实施阶段与评审门禁

**本计划现已批准。按 P0 开始实施；后续阶段完成状态以实施记录和实际测试为准。**

| 阶段 | 主要工作 | 产出与退出条件 | 参考工作量 |
| --- | --- | --- | --- |
| P0：兼容性验证 | 重新锁定版本；核对双入口职责、路由/认证、v8 边界、插件与存储；验证 SDK 挂载与进程内调用 | 最小单进程单端口原型、功能/补丁清单、关键风险清零或再次评审 | 1–2 人日 |
| P1：工程与生命周期 | 上游快照/补丁机制、唯一入口、Manager 嵌入包、上下文/就绪/有界清理 | 单程序启动；所有必要任务启动一次；异常可退出；不留第二个业务监听器 | 2–3 人日 |
| P2：路由与安全集成 | 所有权分发、内部 Transport、凭据分离、流式/WS、v8/插件边界 | 同端口页面/API可用；无递归、无越权；关键协议测试通过 | 3–4 人日 |
| P3：前端与数据 | 内嵌 Manager 页面、同源默认、用量与自动化、配置回读、现有数据导入 | 管理功能闭环，真实前端资源；迁移和缺 key 等负例通过 | 2–3 人日 |
| P4：更新与发行 | 单侧/双侧同步、stable/edge/pinned、CI门禁、各平台构建和整包更新信息 | 任一上游更新后能产出可追溯候选构建；冲突不影响稳定版本 | 2–3 人日 |
| P5：验收与首发 | 全链路、安全、稳定性、跨平台与升级回滚演练；整理文档 | 验收清单通过；发布审批完成；已知限制明确 | 2–3 人日 |

合计约 **12–18 人日**，不含外部评审等待、供应商账号准备以及重大上游破坏性变更；建议额外保留 20%–30% 缓冲。此为规划估算，不是承诺工期，P0 后重新估算。

## 9. 测试与可验收标准

### 9.1 必须通过

- [ ] 启动的是一个新业务进程，不依赖原版 CPA/Manager 的另一个运行实例。
- [ ] 页面、推理 API、管理 API共用同一业务端口；默认没有第二个 Manager 端口和额外调试端口。
- [ ] 管理页是 CPA-Manager-Plus 真实构建结果，根路径、旧页面入口、静态资源和刷新可用。
- [ ] OpenAI、Claude/Gemini 等纳入首期矩阵的协议请求与错误返回保持兼容；SSE、取消和 WebSocket不被分发层缓冲或破坏。
- [ ] 数据面 API key、Manager 管理员凭据、内部核心管理凭据分别校验；伪造内部标记和未授权的 v8 路径无法绕过保护。
- [ ] 认证文件的增删改和账户自动化仍经过 Manager 原有保护，没有旁路。
- [ ] 页面修改配置后可以读回并在核心实际生效；不因重启或配置热更新重复注册任务。
- [ ] 用量采集、持久化、查询、聚合和自动化形成闭环；异常/积压/崩溃窗口与上游语义一致并有记录。
- [ ] 支持在副本上导入原配置、auth 与 Manager 数据；已加密数据缺少原 key 时安全失败。
- [ ] 能分别执行一次仅升级核心、仅升级 Manager、双侧升级的演练，并由锁定清单重构建。
- [ ] 模拟补丁冲突、测试失败、网络不可用时，候选流程安全停止，运行中版本和用户数据不受影响。
- [ ] 能安装新产品包、运行健康检查并按文档回退；涉及数据库迁移时验证匹配备份恢复。
- [ ] 建议平台矩阵构建与运行检查通过；每个平台的插件/CGO差异明确。
- [ ] 版本信息能追溯双上游完整 SHA、补丁、产品版本和工具链；两个上游的许可信息保留。

### 9.2 测试层次

- 单元测试：路由匹配、权限、路径规范化、内部目标限制、取消/超时、补丁和锁定清单。
- 契约测试：Manager 调用的核心接口、v0/v8差异、队列事件结构、版本和能力探测字段。
- 集成测试：配置热更新、数据库迁移/锁、后台任务完整生命周期、异常退出、优雅停止。
- 前端测试：沿用上游类型检查、lint和相关测试，再补同源连接与产品版本/更新入口测试。
- 端到端测试：启动真实合并程序，通过真实端口验证前端、API、SSE/WS和管理闭环。
- 性能对比：相同版本、机器和负载下对比独立核心与合并程序的非流式延迟、首包、流式吞吐和内存；P0制定可复现预算，不先填写没有数据支撑的指标。
- 稳定性演练：建议至少24小时的混合负载，覆盖采集、热更新、后台任务和重启；观察内存、goroutine、数据库锁与错误率。
- 供应商联调：CI优先使用可重放样例和模拟服务；真实OAuth/付费模型测试需用户授权和账号。未实际联调的供应商明确标注，不用模拟结果宣称全部真实供应商可用。

## 10. 主要风险与处置

| 风险 | 影响 | 处置 |
| --- | --- | --- |
| v8管理模型持续变化 | Manager兼容失效或绕过保护 | 显式准入、契约测试，失败阻断更新 |
| SDK没有完整覆盖原启动职责 | “能启动但功能缺失” | 双入口职责清单、差异测试、最小公开桥接 |
| Manager生命周期提取遗漏 | 采集/自动化不运行或停止泄漏 | 与原main逐项核对，启停与故障注入测试 |
| 同路由自代理循环 | 堆栈/资源耗尽 | 内部Transport与私有上下文，递归检测测试 |
| 多种HTTP客户端未统一注入 | 偷连网络、少数功能失效 | 全量CPA调用清单，禁止未注入的内部目标外联 |
| 薄补丁随上游漂移 | 更新维护成本增大 | 补丁职责单一、严格重放、定期寻求上游公开接口支持 |
| 静态网页独立更新 | 前后端版本错配 | 只整包更新，禁用旧控制台覆盖路径 |
| SQLite/密钥迁移不完整 | 数据损坏或无法解密 | 一致性快照、保留原数据、迁移前验证和恢复演练 |
| 仅回退二进制 | schema不兼容 | 把程序、配置、数据库快照作为配套回退单元 |
| CGO/平台插件差异 | 程序可构建但部分能力不可用 | 平台功能矩阵、原生工具链验证，不静默裁剪 |
| 直接追随main | 不稳定版本进入生产 | edge隔离、验证/审批、保留已验证stable |
| 队列出队与持久化窗口 | 非正常退出可能丢失事件 | 先核实上游语义，明确保证边界，强一致需求另行设计 |

## 11. 已批准的默认决策

以下推荐默认值已随本计划获得用户批准；任何实质性范围变化仍需另行评审：

1. **合并形态**：接受单业务进程、单对外业务端口、内嵌前端；不采用Nginx拼接或双进程守护包装。
2. **源码组织**：接受双上游原始快照 + 薄补丁 + 锁定清单；优先使用现成SDK，必要时仅补公开桥接。
3. **更新策略**：stable默认，edge支持main最新SHA；自动发现和构建，正式发布及生产替换默认需人工确认。
4. **前端范围**：复用Manager现有界面，允许统一产品标识、同源连接和整包更新入口的最小适配。
5. **首期平台**：Windows amd64、Linux amd64/arm64及Linux容器；其他平台与插件功能按P0矩阵确认。
6. **数据与运行边界**：默认文件认证和Manager SQLite；已有数据采用备份/副本迁移。额外存储、控制平面、CLI/TUI保留需求在P0明确，不擅自移除或承诺已兼容。
7. **单端口与自动化边界**：OAuth本地临时回调的必要例外需单独列出；首期不做运行时在线编译、自覆盖生产和零停机热升级。

已按 P0 开始实施；若后续证明推荐架构不可行、要求扩大功能裁剪或扩大核心补丁范围，则先回到评审，不自行改变目标。

## 12. 官方资料与源码索引

版本信息通过以下官方API在2026-11-03 22:46（UTC+08:00）核验：

- [CLIProxyAPI最新正式发布API](https://api.github.com/repos/router-for-me/CLIProxyAPI/releases/latest)
- [CLIProxyAPI main提交API](https://api.github.com/repos/router-for-me/CLIProxyAPI/commits/main)
- [Manager最新正式发布API](https://api.github.com/repos/seakee/CPA-Manager-Plus/releases/latest)
- [Manager main提交API](https://api.github.com/repos/seakee/CPA-Manager-Plus/commits/main)

锁定基线的关键源码（避免main变化导致评审引用漂移）：

### CLIProxyAPI

- [v8.0.53正式发布](https://github.com/router-for-me/CLIProxyAPI/releases/tag/v8.0.53)
- [Go module / 工具链](https://github.com/router-for-me/CLIProxyAPI/blob/01b24f1f95dc9b309765212c52b25b25e005ff01/go.mod)
- [嵌入Builder](https://github.com/router-for-me/CLIProxyAPI/blob/01b24f1f95dc9b309765212c52b25b25e005ff01/sdk/cliproxy/builder.go)
- [公开API扩展点](https://github.com/router-for-me/CLIProxyAPI/blob/01b24f1f95dc9b309765212c52b25b25e005ff01/sdk/api/options.go)
- [SDK运行生命周期](https://github.com/router-for-me/CLIProxyAPI/blob/01b24f1f95dc9b309765212c52b25b25e005ff01/sdk/cliproxy/service_lifecycle.go)
- [HTTP服务器/共享监听器](https://github.com/router-for-me/CLIProxyAPI/blob/01b24f1f95dc9b309765212c52b25b25e005ff01/internal/api/server.go)
- [原生路由及回调](https://github.com/router-for-me/CLIProxyAPI/blob/01b24f1f95dc9b309765212c52b25b25e005ff01/internal/api/server_routes.go)
- [核心原始入口职责](https://github.com/router-for-me/CLIProxyAPI/blob/01b24f1f95dc9b309765212c52b25b25e005ff01/cmd/server/main.go)
- [配置模板](https://github.com/router-for-me/CLIProxyAPI/blob/01b24f1f95dc9b309765212c52b25b25e005ff01/config.example.yaml)
- [许可证](https://github.com/router-for-me/CLIProxyAPI/blob/01b24f1f95dc9b309765212c52b25b25e005ff01/LICENSE)

### CPA-Manager-Plus

- [v1.14.10正式发布](https://github.com/seakee/CPA-Manager-Plus/releases/tag/v1.14.10)
- [管理端Go module](https://github.com/seakee/CPA-Manager-Plus/blob/a67ed8834a914ab24c36d2490fa6bb6d4166724c/apps/manager-server/go.mod)
- [前端依赖与构建命令](https://github.com/seakee/CPA-Manager-Plus/blob/a67ed8834a914ab24c36d2490fa6bb6d4166724c/apps/web/package.json)
- [HTTP Handler与静态资源内嵌](https://github.com/seakee/CPA-Manager-Plus/blob/a67ed8834a914ab24c36d2490fa6bb6d4166724c/apps/manager-server/internal/httpapi/server.go)
- [管理路由所有权](https://github.com/seakee/CPA-Manager-Plus/blob/a67ed8834a914ab24c36d2490fa6bb6d4166724c/apps/manager-server/internal/http/router/router.go)
- [管理代理与v8准入保护](https://github.com/seakee/CPA-Manager-Plus/blob/a67ed8834a914ab24c36d2490fa6bb6d4166724c/apps/manager-server/internal/service/proxy/service.go)
- [管理代理鉴权](https://github.com/seakee/CPA-Manager-Plus/blob/a67ed8834a914ab24c36d2490fa6bb6d4166724c/apps/manager-server/internal/http/controller/proxy/handler.go)
- [原始启动与后台任务](https://github.com/seakee/CPA-Manager-Plus/blob/a67ed8834a914ab24c36d2490fa6bb6d4166724c/apps/manager-server/cmd/cpa-manager-plus/main.go)
- [用量采集模式](https://github.com/seakee/CPA-Manager-Plus/blob/a67ed8834a914ab24c36d2490fa6bb6d4166724c/apps/manager-server/internal/collector/collector.go)
- [HTTP用量队列客户端](https://github.com/seakee/CPA-Manager-Plus/blob/a67ed8834a914ab24c36d2490fa6bb6d4166724c/apps/manager-server/internal/httpqueue/client.go)
- [前端连接默认值](https://github.com/seakee/CPA-Manager-Plus/blob/a67ed8834a914ab24c36d2490fa6bb6d4166724c/apps/web/src/utils/connection.ts)
- [前端独立更新索引](https://github.com/seakee/CPA-Manager-Plus/blob/a67ed8834a914ab24c36d2490fa6bb6d4166724c/apps/web/src/services/api/version.ts)
- [构建与内嵌资源装配](https://github.com/seakee/CPA-Manager-Plus/blob/a67ed8834a914ab24c36d2490fa6bb6d4166724c/Dockerfile.manager-server)
- [许可证](https://github.com/seakee/CPA-Manager-Plus/blob/a67ed8834a914ab24c36d2490fa6bb6d4166724c/LICENSE)

公开 CI 仅按白名单导出构建身份、校验和及候选验证元数据，不自动上传二进制、完整源码、安装归档或宽泛日志/报告目录；整包再分发仍须单独完成安全与正式发布审批。
