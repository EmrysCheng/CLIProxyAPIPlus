# CLIProxyAPIPlus 实施状态

更新日期：**2026-11-06（Asia/Shanghai）**。当前版本为 **`0.4.0`，正式源码发行**。版本通道、可下载附件和生产部署验收分别记录：本次不附带未完成发布签收的二进制包，不宣称全部生产门禁已通过。

- [使用指南](https://github.com/EmrysCheng/CLIProxyAPIPlus/blob/master/docs/USER_GUIDE_CN.md)
- [双上游源码自更新](SOURCE_UPDATES_CN.md)
- [v0.4.0 发布说明](https://github.com/EmrysCheng/CLIProxyAPIPlus/releases/tag/v0.4.0)
- [发行与生产部署验收清单](RELEASE_CHECKLIST_CN.md)

## 1. 已实现的整合能力

当前实现已超出初始架构原型，不以旧原型包充当本次发行物。源码构建会生成包含以下能力的整合程序：

| 范围 | 当前实现 |
| --- | --- |
| 业务入口 | CLIProxyAPI 核心与 Manager 共用一个业务进程、一个对外业务端口 |
| 管理页面 | 实际构建并嵌入 CPA-Manager-Plus 原有 React 前端 |
| 生命周期 | 嵌入/独立 Manager 共用初始化、数据库锁、后台任务与有界排空 |
| 鉴权与内部调用 | 管理员、客户端与内部管理凭据分离；专用注入 transport，不替换全局 HTTP 客户端 |
| 配置边界 | 页面与 watcher 共用校验；监听、TLS、认证目录、内部密钥等固定设置需停机变更 |
| 日志与时区 | 核心与 Manager 共享轮转文件日志，程序内嵌 IANA 时区数据 |
| 数据维护 | 导入、SQLite 一致性备份、完整性校验、新路径恢复和程序/数据成对回滚 |
| 上游维护 | 单侧或双侧固定提交、严格补丁、隔离验证、事务源码晋级及中断恢复 |
| 页面更新 | 默认检查两个官方上游正式发布；管理员确认后隔离源码构建、验证、安装与回滚 |
| 随包工具 | 完整 Portable 构建产物含固定且校验过的 Python、Go、Node、npm |
| 构建身份 | 产品源码摘要、lock、双上游完整提交、二进制摘要与匹配源码配方 |
| 交付工具 | 三平台 Portable / Linux Full 构建入口、CI、容器配方、SBOM 与许可资料 |

## 2. 当前使用方式

推荐独立 Portable：

```powershell
.\cliproxyapi-plus.exe --config config.yaml --data-dir data --web-updates --source-updates
```

不需要 `release-index`、`index.json` 或额外发行服务。源码更新执行的是管理员确认的固定上游代码，工具内置不等于依赖离线；隔离目录和摘要校验不构成 OS 沙箱。

完整安装目录须保留 `build-tools/`、`update-tools.json`、`updater-source/`。默认工具配置含 `go_proxy: https://goproxy.cn|https://proxy.golang.org`。产品换版须使用匹配的完整包，不只替换 EXE、不混用配方、不重复初始化已有数据。

## 3. 当前支持边界

- 文件认证存储、本地 SQLite；Home、PG/Git/对象 token store、CLI/TUI、mDNS、pprof 没有在整合入口提供。
- Portable 禁用动态插件；Linux Full/CGO 与插件能力需独立原生验证。
- OAuth 和更新测试可能使用临时回环回调/测试端口，不承诺整个生命周期仅一个 socket。
- 页面安装器仅供满足安全检查的独立 Portable 实例，不与 Windows Service、systemd、容器自动拉起混用。
- 回滚恢复匹配快照，不能合并快照之后的业务写入；私有更新目录可能保存活动配方，不可当缓存随意删除。
- 网页维护模式要求 KVStore 状态位于受备份覆盖的 data 内，拒绝链接/junction、主目录展开及 Windows NTFS 命名流等不完整快照路径。
- 已登记 CLIProxyAPI v8.0.55 的严格补丁前像，但当前锁定基线仍为 v8.0.53 / Manager v1.14.10；不承诺任意未来上游都无需适配。

## 4. 验证事实与已知问题

2026-11-06 对发布前基线提交 `822bde3f575e11178ec6f4f5e2df8bb172578ea9` 的 GitHub Actions 做了只读核对，运行 [42068706538](https://github.com/EmrysCheng/CLIProxyAPIPlus/actions/runs/42068706538) 结果如下：

| Job | 结果 | 范围/失败步骤 |
| --- | --- | --- |
| Linux amd64 Portable | 通过 | 该提交对应工作流配置的步骤 |
| Linux arm64 Portable | 通过 | 该提交对应工作流配置的步骤 |
| Windows Portable | 失败 | `project.py test-source-build`；不能据此宣称完整源码更新流水线通过 |
| Linux Full | 失败 | `regression.py --race`；步骤失败不等于已经确认数据竞争 |

这些是**发布前基线**的结果，不自动转移为 0.4.0 新提交的通过证据。版本元数据修订后的本地验证与发布提交 CI，以对应 Release 的验证附件和当前提交工作流为准；不重用旧报告冒充新版本验收。

工程回归、二进制烟测或两个 Linux Portable Job 通过，均不能代替：

- 经授权的真实供应商/OAuth、state/nonce/重放和真实认证联调；
- 用户历史库、认证副本的迁移、schema/加密兼容与业务回读；
- 24h 混合故障、重启、热更新、长连接及资源趋势验证；
- 所有平台/Full/插件/容器与性能预算的运维签收；
- 依赖声明、缺失 notice、漏洞、安全、第三方 OAuth 再分发和新增产品代码许可证决策。

## 5. 公开源码与发行范围

公开 Git 仅保存产品、补丁和固定锁，不提交原始 `upstream/` 快照、运行数据、秘密或私有报告。首次克隆用 `python scripts/hydrate_upstreams.py` 补齐官方固定提交，核对树摘要、模块身份和 LICENSE；已有快照不覆盖。

本次以 `v0.4.0` 正式源码 Release 发布，不把未签收二进制或含完整上游配方的归档作为附件。公开 CI 也只按精确白名单导出验证元数据。未来公开完整安装包须单独满足再分发及平台验收，不能仅凭去掉版本后缀认定完成。

## 6. 历史记录

本文改为现行能力和验收状态索引，避免早期启动方式及旧版本状态混入当前指南。逐阶段原始记录保留在 [Git 历史](https://github.com/EmrysCheng/CLIProxyAPIPlus/blob/822bde3f575e11178ec6f4f5e2df8bb172578ea9/docs/IMPLEMENTATION_STATUS_CN.md)，包括已知失败和当时门禁；此次文档整理没有将历史失败改写为通过。
