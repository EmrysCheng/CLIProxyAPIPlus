# 更新记录

## [0.4.0](https://github.com/EmrysCheng/CLIProxyAPIPlus/releases/tag/v0.4.0) — 2026-11-06

首个正式版本号的源码发行，不附带未完成签收的二进制安装包。

- CLIProxyAPI 与 CPA-Manager-Plus 后端及原前端源码整合，共用业务进程和端口。
- 双上游页面检查、隔离源码构建、验证、升级和程序/数据成对回滚；完整 Portable 构建配方内置 SDK。
- 更新 README、安装使用、费用估算、日志、运维和开发文档。
- 产品及独立发行服务的运行版本通道跟随实际构建版本，不再硬编码为预发布。
- 公开源码按固定完整提交补齐原始上游快照；CI 保留元数据导出白名单。

已知 CI 问题和未完成生产验收见[完整发布说明](docs/releases/v0.4.0.md)及[验收清单](docs/RELEASE_CHECKLIST_CN.md)。历史开发记录保留于 Git 历史，不将历史失败改写为通过。
