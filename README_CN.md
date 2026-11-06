# CLIProxyAPIPlus · 中文快速使用

当前版本：**`0.4.1`。** [完整 Portable 下载](https://github.com/EmrysCheng/CLIProxyAPIPlus/releases/tag/v0.4.1) 提供 Windows amd64 ZIP、Linux amd64 / arm64 TAR.GZ 及 SHA256 附件。请按系统/架构选择并先校验摘要；GitHub 自动生成的 Source code 不是程序包。支持范围与生产部署边界见[验收清单](docs/RELEASE_CHECKLIST_CN.md)。

CLIProxyAPI 核心、CPA-Manager-Plus 管理后端和原 React 前端编译为一个 Go 程序，共用一个对外业务端口。此文件也随 Portable 包分发；仓库首页见 [README.md](https://github.com/EmrysCheng/CLIProxyAPIPlus/blob/master/README.md)，完整操作见[详细使用指南](https://github.com/EmrysCheng/CLIProxyAPIPlus/blob/master/docs/USER_GUIDE_CN.md)。

## 首次安装

在完整 Portable 包的解压目录执行，保留 `build-tools/`、`update-tools.json`、`updater-source/`。已有实例不要重新初始化。

```powershell
# Windows：仅全新 config/data 执行一次
.\cliproxyapi-plus.exe --init --config config.yaml --data-dir data --listen 127.0.0.1:8317
.\cliproxyapi-plus.exe --config config.yaml --data-dir data --web-updates --source-updates
```

```sh
# Linux：仅全新 config/data 执行一次
./cliproxyapi-plus --init --config config.yaml --data-dir data --listen 127.0.0.1:8317
./cliproxyapi-plus --config config.yaml --data-dir data --web-updates --source-updates
```

打开 `http://127.0.0.1:8317/`，使用 `data/admin.key` 中的初始凭据登录。配置供应商/认证后，模型客户端使用 `http://127.0.0.1:8317/v1` 与初始 `data/api.key` 或配置中有效的客户端 key。不要将 `core-management.key` 用于浏览器或模型客户端；`data.key` 必须与数据库一起备份。管理员后续改密以 Manager 数据库为准，不由提示文件自动覆盖。

默认回环监听；外部访问需要显式 `--allow-remote` 及 TLS、访问控制、防火墙。更改监听/TLS/认证目录需要停机。常规独立运行不需要页面更新时，可省略 `--web-updates --source-updates`。

## 双上游页面更新

推荐方式：使用上面两个更新开关，进入“系统信息 → 程序更新”，检查并确认单侧或双侧源码更新。**不需要 release-index、index.json 或独立发行服务。** 不要与 Windows Service、systemd、容器自动重启等机制混用。

完整 Portable 包内置 Python/Go/Node/npm。源码与依赖仍需网络，默认配置为：

```json
{
  "bundle": "./build-tools",
  "go_proxy": "https://goproxy.cn|https://proxy.golang.org"
}
```

非空 `go_proxy` 优先于启动环境中的 `GOPROXY`；删除/置空才继承环境。包内 `GOENV=off`，不要用 `go env -w` 代替本配置，也不要用空的 `update-tools.example.json` 覆盖实际配置。

先备份再升级；页面阶段数不是耗时百分比。回滚恢复旧程序及匹配数据快照，不合并快照后的新增数据。不要删除 `<data-dir>.updates`，其中可能有回滚快照及后续构建使用的配方。只更新上游不等于获取本整合仓库未来全部产品变更，产品换版须使用匹配的完整包。

## 日志、费用与维护

- 默认共享文件日志：`logging-to-file: true`、`logs-max-total-size-mb: 100`。准确位置看启动输出 `Application log:`；配置日志基目录用 `WRITABLE_PATH`，不能假设总在安装目录。
- 用量正常但预计花费为零时，进入 `/#/model-prices` 配置费率、确认模型名/别名匹配并等待重算；未计价不等于免费，估算不是实际账单。
- 初始化不自带供应商账户；API 返回无可用模型时先检查供应商与认证配置。
- 停止所有写入进程后使用 `tools/data.py` 备份/恢复。维护脚本可用包内 Python 运行，参数及数据恢复限制见下表。
- 发现 `.maintenance` 或 `recovery_required` 时保留现场，按运维文档恢复；不要直接删锁、只换 EXE 或盲目重试。

## 文档导航

以下文档随完整 Portable 包分发，也可在仓库阅读：

- [双上游源码自更新](docs/SOURCE_UPDATES_CN.md)：页面升级、工具、网络与回滚。
- [运维与数据迁移](docs/OPERATIONS_CN.md)：备份、恢复、导入及离线维护。
- [实施状态](docs/IMPLEMENTATION_STATUS_CN.md)与[发行检查清单](docs/RELEASE_CHECKLIST_CN.md)：功能边界及未完成验收。
- [高级兼容：整包索引更新](docs/WEB_UPDATES_CN.md)、[本机 HTTP 索引](docs/LOCAL_WEB_UPDATES_CN.md)、[独立发行服务](docs/RELEASE_SERVER_CN.md)：均非默认部署要求。

仓库内补充文档：[完整使用指南](https://github.com/EmrysCheng/CLIProxyAPIPlus/blob/master/docs/USER_GUIDE_CN.md)、[开发指南](https://github.com/EmrysCheng/CLIProxyAPIPlus/blob/master/docs/DEVELOPMENT_CN.md)、[文档索引](https://github.com/EmrysCheng/CLIProxyAPIPlus/blob/master/docs/README.md)。首次源码克隆须先 `python scripts/hydrate_upstreams.py`，再 `python scripts/project.py prepare` 和 `python scripts/project.py build`；不要将原始 `upstream/` 强行加入公开 Git。

许可与发行限制见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。本文件不为新增产品代码指定许可证，不代表第三方源码、安全、许可或生产发布审批已经完成。
