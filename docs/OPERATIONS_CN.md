# 运维、数据迁移与双上游更新

适用版本：`0.4.0-rc.6`（候选版，未完成全部生产验收）；保留早期离线维护及整包索引模式说明。默认页面更新方式见 [双上游源码自更新](SOURCE_UPDATES_CN.md)，首次使用见[详细使用指南](https://github.com/EmrysCheng/CLIProxyAPIPlus/blob/master/docs/USER_GUIDE_CN.md)。命令从源码工作区执行；常规业务运行不依赖系统 Python/Go/Node，维护脚本需要 Python 3.12+（完整 Portable 包可使用内置 Python）。发行 ZIP/tar 中附带 `tools/data.py`、`tools/upgrade.py`、`tools/private.py`、`tools/archive_paths.py`，使用时把下文 `scripts/` 改成 `tools/`，或继续从源码工作区运行。所有示例路径应替换成真实部署路径，先在副本上演练。

## 1. 配置职责

- `config.yaml`：核心原生 YAML，保留供应商、模型映射、API keys 等原规则。
- Manager SQLite：用量、仪表盘、任务、自动化与受保护连接设置，复用原 schema 与迁移。
- `admin.key`：首次管理员凭据；产品接口使用 Manager 当前数据库中的管理员验证器，而非永久硬编码比较这个文件。
- `api.key`：初始化/导入时提供的首个客户端 key 提示文件；日常客户端 key 仍按原生配置管理。
- `core-management.key`：仅内部进程内管理使用，不能被面板旋转成独立 CPA 地址或外部密码。
- `data.key`：Manager 加密材料；丢失可能使旧连接数据不可解密。

监听器/TLS、认证目录、内部管理隔离、插件开启状态在本次运行固定。页面写 YAML 与文件 watcher 共用核心配置校验边界；危险修改不能先落盘再悄悄忽略。要修改固定设置，停止服务、编辑相应配置和凭据、验证后重启。

### 1.1 运行日志

rc.2 合并入口在核心/Manager 启动前配置上游文件轮转，并将 Manager 的标准库日志转入共享 logger。默认 `logging-to-file: true`、`logs-max-total-size-mb: 100`，缺省补齐只作用于运行时，不为补默认值重写旧配置；显式 `false`/`0` 保留含义。初始化会写出这两个字段。默认变更同时覆盖磁盘加载和上传解析，避免热更新回到旧缺省值。

`main.log` 单文件 10 MB，旧日志每分钟按总量目标清理，当前活跃日志受保护。目录优先 `WRITABLE_PATH/logs`，否则为工作目录中已存在且可写的 `logs`，再回退到原生 `auth-dir/logs`；以启动时打印的绝对路径为准。需要固定目录时先设置 `WRITABLE_PATH`（不是 `ZONEINFO`）。日志目录中不要放置无关 `.log` 文件，上游清理器会按后缀识别旧日志。文件不可打开时启动失败并在控制台显示错误；后续运行期磁盘耗尽仍需运维告警与容量监控。现有日志读取/下载接口及文件/控制台热切换沿用核心能力。

## 2. 旧部署导入

停止源 CPA 和源 Manager，确认没有其他程序写入源数据库/认证目录。原始源路径只读；源程序也必须停止，不能只靠目标锁推断它已停止。

```powershell
python scripts/data.py import --native-config <old-config.yaml> --auth-dir <old-auth-directory> --manager-data-dir <old-manager-data-directory> --destination <new-installation-directory> --listen 127.0.0.1:8317
```

- 目标目录必须不存在；不覆盖现有部署。
- 源 Manager 必须为当前支持的本地 SQLite 布局（`usage.sqlite`、`data.key`），不同上游历史版本要先用其官方迁移路径在副本升级。
- 导入保留原核心配置与客户端 key，重新生成内部管理与管理员 key，复制认证文件、任务/用量库及其加密 key。
- 目标的管理员 hash 会离线重设为目标新凭据，避免“文件是新密码、数据库仍是旧密码”。
- TLS 路径会被解析到原证书路径；要搬离源机器，应先在目标离线备份/恢复，使证书与私钥纳入新目录。
- 原 Home/可选存储配置不应误以为已受支持；不要启动含未支持模式的配置。失败的半成品目标保留供诊断，修复后选择另一个新目标，不覆盖重试。

启动目标后核对页面、提供商、认证、客户端鉴权、用量/任务状态；只完成模拟数据导入不代表真实业务数据已获验收。

## 3. 一致性备份 / 恢复

```powershell
# 停止服务后执行，snapshot 必须在 data-dir 外且尚不存在
python scripts/data.py backup --binary <installed-binary> --config <config.yaml> --data-dir <data-directory> --snapshot <private-backup.zip>
python scripts/data.py verify --snapshot <private-backup.zip>
python scripts/data.py restore --binary <installed-binary> --snapshot <private-backup.zip> --config <new-config.yaml> --data-dir <new-data-directory>
```

Python 使用和 Go Manager 相同的 OS 文件锁；数据库通过 SQLite backup API 生成一致性副本并运行 quick_check。备份覆盖配置、SQLite、数据密钥、三种凭据、认证目录、任务归档及启用的 TLS 材料。每个条目有 SHA256/大小，恢复前验证完整清单、路径、重复条目、符号链接和大小上限。

**备份 ZIP 是秘密载体，不是公开发布物。** 工具用私有暂存目录和文件权限；临时目录及失败数据有意保留，审计后由操作者安全清理。复制/上传到其他文件系统后重新确认 ACL；不具备完整性信任的备份即使清单自洽也不可信。

恢复只发布到新路径并重定位认证/TLS 路径。它不能代替双目录原子切换；中途故障留下部分目标时不要启动它，保留证据并用新的目标恢复。禁止在未确认停机时绕过 `.manager.lock` 或删除 `.maintenance`。

## 4. 整包程序升级和成对回滚

本节 `upgrade.py` 是离线主程序/数据维护工具，**不会自动安装新的 SDK、匹配源码配方或其余完整包内容**。0.4.x 的产品换版应先保留旧完整目录，将可信新完整包解压至新目录，使用原 config/data 路径受控迁移并验证；不要以只替换 EXE 作为升级完成。独立 Portable 的双上游页面更新采用第 9 节与专用事务引擎，不使用本节脚本拼接不同版本的配方。

1. 确认来源、签名/可信校验和及发行说明，尤其是 schema 迁移；没有正式签名时由内部受控渠道确认 SHA256。
2. 停止服务；保留数据目录与安装路径，不在运行中替换 exe。
3. 先 dry-run，再显式执行：

```powershell
python scripts/upgrade.py install --binary <installed-binary> --candidate <new-binary> --sha256 <trusted-binary-sha256> --config <config.yaml> --data-dir <data-directory> --state-dir <private-upgrade-history>
# 审核上面的计划后再增加 --apply
```

4. 维护标记阻止产品并发正常启动。工具备份旧程序与整套数据，核对候选 checksum，原子替换程序，使用新程序离线 `--check-data` 运行原生迁移/完整性检查。
5. 迁移/校验失败自动恢复**匹配的旧程序和数据快照**；不允许只回滚 exe 后让它打开新 schema。失败现场保存在 `.failed-<id>` 兄弟路径。若原认证/TLS 材料位于 data-dir 外，回滚会把配置重定位到快照恢复的 data/auths、data/tls，防止继续引用升级后已变化的外部文件；不会覆盖外部原目录。
6. 成功状态为 `awaiting_restart_and_health_check`，不是已经完成线上健康检查。由服务管理器重启后执行：

```powershell
python scripts/upgrade.py verify-running --url http://127.0.0.1:8317 --version <实际目标整合版本>
# 若需撤回，先停止服务
python scripts/upgrade.py rollback --journal <upgrade-history/id/journal.json>
```

回滚信任私有 journal 中的原始绝对路径和备份摘要，不执行来源不明的 journal。进程被终止留下其自身维护标记时，可用对应回滚 journal 恢复；不盲目删除不认识的 nonce。恢复/回滚没有提供跨设备分布式事务，磁盘/权限故障需人工处理。

## 5. 双上游候选与晋级

`stage` 会把产品和双快照复制到 `.build/candidates/<id>/source`，按官方仓库的确定提交下载，验证模块身份、LICENSE 和确定性树摘要。压缩包禁止路径逃逸、`.git`、符号链接及超量内容。

`validate` 清除 token/secret 等敏感环境变量，运行构建、E2E、完整前端 lint/test、适配回归与两个原始入口编译、二进制/维护冒烟，逐步写回 `validation.json`。构建不会自动执行 npm 安装脚本。这里的适配回归不是“全平台、全部上游测试都通过”的替代物。

- 适配前像变化立即失败，不模糊匹配；比较候选源文件，更新 `patches/` 的严格变换后重新 stage。
- 候选期间产品/补丁/文档变动、上游树变动或当前基线 lock 改变都会使回执失效，必须重做候选。
- `promote` 在互斥 OS 锁下先复制候选，再记 journal、交换两份快照，最后替换 lock；中断恢复用 `python scripts/upstreams.py recover --journal <promotion-journal>`。
- 晋级只操作源码，不安装、不推送、不发布。原始两个 LICENSE 保留。
- 每日工作流只导出白名单候选/验证元数据，不公开完整候选源码或安装包；发布必须另走可信审批，不能给上游测试授予生产凭据或 write token。

## 6. Docker / Compose（需原生验证）

Dockerfile 是单程序、多阶段编译，最终阶段复制完整产物目录，当前也包含随包构建工具，但常规业务入口不运行 Node/Go；Compose 只映射一个业务端口，默认宿主回环、非 root、只读根文件系统。以下步骤是需要在目标环境验证的操作路径，不是已通过容器生产验收的证据。

```sh
mkdir -p runtime
# Linux bind mount 必须允许容器 UID 10001 写入；按部署权限设置所有者
sudo chown 10001:10001 runtime
docker compose build
docker compose run --rm --no-deps cliproxyapi-plus --init --config /state/config.yaml --data-dir /state/data --listen 0.0.0.0:8317
docker compose up -d
```

容器启动显式 `--allow-remote` 是为了监听容器网卡，宿主发布仍默认 `127.0.0.1:8317`。若改成公网映射，必须自行补充 TLS/访问边界。首次初始化与升级需停机；不要把 Docker 的 restart 策略当成迁移/回滚协议。

## 7. 稳定性与诊断

```powershell
# 本机模拟负载，默认 60 秒；不接付费供应商
python scripts/soak.py --seconds 86400 --workers 4
# 先用 regression.py 构建原始 core，再作本机顺序诊断对比
python scripts/benchmark.py --core-binary .build/regressions/core.exe --samples 200
```

`soak` 定期使用管理员鉴权的 `/plus-api/runtime` 采集 goroutine、heap/GC，混合 JSON/SSE、models、用量读取，最终验证 SQLite 用量。延迟保留最近 100000 条样本，日志丢弃以避免 24h 无界磁盘增长；短测不能代替24h、热更新/重启混合场景或生产 SLO。

公开健康接口：`GET /plus-api/ready`、`GET /plus-api/version`；管理员接口：`/plus-api/releases`、`POST /plus-api/releases/check`、`GET /plus-api/runtime`。这些接口不包含认证秘密，运行时诊断不开放 pprof。

构建默认使用工作区私有 Go cache；重复验证同一可信源码时可显式设置 `CLIPROXY_PLUS_GOCACHE` 指向可信的本地缓存目录，不改变源码/补丁/回执校验。不要使用来源不明或共享给不可信写入者的编译缓存。Linux tar 包明确设置程序为 0755，不依赖 Windows 文件权限映射。


## 8. 网页提醒、升级与回滚（0.3）

新增功能不替代第 5 节的双上游源码适配/验证；网页只安装本产品整包，不安装两个原版项目，不运行 Git/npm/编译器。需要先由已审核流水线构建、托管整合包与可信索引（默认 HTTPS，本机 HTTP 需显式开启）。`scripts/release_index.py` 可生成 schema 1 索引，但不上传/签名，默认从校验后的安装包读取真实产品源码 SHA256；Git commit 可选，不能伪造。`--release-index` / `CLIPROXY_PLUS_RELEASE_INDEX` 仍由启动操作者设置，网页不能提交任意 URL。

- 未加 `--web-updates`：仍显示版本/提醒和历史；执行按钮禁用，离线工具照常使用。
- 加 `--web-updates`：仅用于独立 Portable 用户进程，自包含 auth/TLS 路径和可写安装目录。识别到 Windows Service、systemd/Kubernetes/容器时禁用；不要手工绕过或混用外部自动重启。未知第三方守护进程无法完全自动识别，需操作者确认。
- 私有索引使用自建 CA 时，增加 `--release-ca-file <CA.pem>`，只影响专用发行传输，不修改全局 TLS/HTTP 客户端。包必须同索引 HTTPS origin，或来自明确允许的 GitHub release/asset 域名；跨域重定向仍校验，不支持任意镜像跳转。
- 后台启动时/每 6 小时检查，页面提醒约每 60 秒刷新；更新页每 3 秒刷新操作状态。检查失败的陈旧元数据不能安装。只检查不自动升级。
- 升级前输入目标版本。下载阶段失败不停止旧程序；握手成功后才请求正常排空，临时辅助进程等待旧进程实际退出。单端口仍由业务进程持有，不增加维护 HTTP 端口。
- 更新后新业务实例在后台运行，控制台启动信息保留于该次操作的私有 `application-console.log`，常规运行日志仍使用配置的 `main.log`。按部署用户管理这一后台进程；不要用 init 初始化已有数据。
- 只有本地记录成功升级且摘要匹配当前程序，页面才提供回滚。输入旧版本并勾选快照恢复确认；操作同时回退程序、配置、SQLite、密钥与认证文件。快照后逻辑变更不会继续存在于恢复后的系统，旧目录另行保留以供人工核查，不自动合并数据库。

### 8.1 私有记录与故障恢复

`<data-dir>.updates/<operation-id>/` 保存 `operation.json`、校验快照、下载包、worker 副本和日志；采用与私有数据一致的目录/文件权限，并核对已有目录和记录的所有者。安装/配置/数据及其父目录不能允许不可信本机账户写入。页面仅返回阶段/版本/受控错误，不返回路径、重启参数或快照内容。SHA 不符、压缩包越界/链接/重复条目、平台/版本/协议不匹配一律拒绝。快照总量上限 16 GiB；包上限 512 MiB，单程序上限 384 MiB。超限或磁盘不足会报错，需先评估部署数据规模。

事务中维护标记阻止普通启动；只有该事务校验通过的一次性激活实例可启动。辅助进程崩溃、断电、无法确认子进程已退出或恢复失败时，不盲目清标记/继续服务。先停止相关业务/辅助进程，并保留整个状态目录。在操作目录中用**已保存的 worker 副本**离线恢复（不要从正在替换的安装路径运行恢复）：

```powershell
& "D:\deploy\data.updates\<operation-id>\worker.exe" --recover-web-update <operation-id> --config "D:\deploy\config.yaml" --data-dir "D:\deploy\data"
# 成功恢复后，仍按原有参数受控启动安装目录中的程序。
```

恢复也使用原生跨进程锁，运行中的数据库不会被替换。无完整快照但能确认原程序未更改时，只清本事务标记；不能确认则保持 `recovery_required`。中断恢复保留已有失败目录并使用新保留目录重试，不递归删除目录。每次更新会保留整套快照，当前没有自动清理策略；离线核查和外部安全备份后再由操作者管理空间。

本机验收入口：先构建当前平台 Portable，再执行 `python scripts/project.py test-update`。也可设置 `CLIPROXY_PLUS_UPDATE_E2E=1` 后执行对应 Go 测试；流水线命令要求新鲜成功回执，跳过测试不能算通过。源码候选 validate 和 CI 已加入这一门禁。只使用临时副本和本地 TLS 发行服务，不升级工作区/用户运行实例；当前实测平台与结果以对应验证报告为准，不据 Windows 结果声称 Linux 原生/容器通过。


### 8.2 本机 HTTP 发行源

0.3.0-rc.2 起可显式加 `--allow-local-release-http`。仅回环 IP 字面量，不允许 DNS/局域网地址；包与重定向必须同索引 HTTP origin，普通 HTTPS 策略不降级。完整配置与错误排查见 [LOCAL_WEB_UPDATES_CN.md](LOCAL_WEB_UPDATES_CN.md)。生成器同名开关只负责生成本机索引，应用必须独立显式开启。`product_source_sha256` 来源于校验包的 build-info，并与候选 binary 身份匹配；commit-only 旧索引仍兼容。升级重启与回滚保留启动开关，拒绝不支持本机 HTTP 开关的候选程序。


## 9. 现行默认：直接更新两个基础项目（0.4.0）

使用 `--web-updates --source-updates`，不配置发行索引，不启动额外发行服务。包内 updater-source 与主程序身份匹配，完整 Portable 包内置受信任且经校验的 Python/Go/Node/npm，不需要用户另外下载；仍须联网获取源码和依赖。页面按固定仓库正式版检查、固定提交、管理员确认执行源码、隔离适配与验证，然后进入原事务安装/回滚。详细步骤和不是 OS 沙箱的信任限制见 [SOURCE_UPDATES_CN.md](SOURCE_UPDATES_CN.md)。第 8 节的外部整包索引方式仅保留为高级兼容选项，不是源码自更新依赖。
