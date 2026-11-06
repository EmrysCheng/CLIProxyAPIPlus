# 更新记录

## [0.4.1](https://github.com/EmrysCheng/CLIProxyAPIPlus/releases/tag/v0.4.1) — 2026-11-06

- 补齐 Windows amd64、Linux amd64、Linux arm64 完整 Portable 发行链路、归档 SHA256 和原生验证回执。
- 区分普通验证与显式发布权限，三包齐全且校验通过后才公开 draft，不覆盖已公开版本。
- Linux race 包级预算增加，保留测试、数据规模和断言；Windows 恢复仅对短暂文件占用进行有界等待。
- 补充源码构建失败白名单诊断；更新 README 下载入口、平台选择、校验与解压说明。
- 修复 CI 中 Windows 临时路径短名称比对异常与上游中断测试异步时序竞争。
- 增加工具链版本探测超时时间，避免 Windows 冷启动 npm 偶发被误判为 missing。
- 保留 v0.4.0 的源码发行事实；工程门禁不等于全部真实生产验收。

## [0.4.0](https://github.com/EmrysCheng/CLIProxyAPIPlus/releases/tag/v0.4.0) — 2026-11-06

首个正式版本号的源码发行，不附带未完成签收的二进制安装包。

- CLIProxyAPI 与 CPA-Manager-Plus 后端及原前端源码整合，共用业务进程和端口。
- 双上游页面检查、隔离源码构建、验证、升级和程序/数据成对回滚；完整 Portable 构建配方内置 SDK。
- 更新 README、安装使用、费用估算、日志、运维和开发文档。
- 产品及独立发行服务的运行版本通道跟随实际构建版本，不再硬编码为预发布。
- 公开源码按固定完整提交补齐原始上游快照；CI 保留元数据导出白名单。

已知 CI 问题和未完成生产验收见[完整发布说明](docs/releases/v0.4.0.md)及[验收清单](docs/RELEASE_CHECKLIST_CN.md)。历史开发记录保留于 Git 历史，不将历史失败改写为通过。
