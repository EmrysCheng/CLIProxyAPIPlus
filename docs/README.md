# 文档目录

适用整合版本：**`0.4.1`**。文档描述当前代码的使用路径，不代表全部生产门禁已经签收。项目概览见[仓库首页](../README.md)。

当前 Release 见[发布说明](releases/v0.4.1.md)：Windows amd64、Linux amd64、Linux arm64 提供独立的完整 Portable 程序包。历史 v0.4.0 仍为源码发行，不追改旧标签。

## 按目标阅读

| 目标 | 首选文档 |
| --- | --- |
| 第一次安装、登录、接入客户端 | [详细使用指南](USER_GUIDE_CN.md) |
| 页面检查两个上游、升级、查看进度、回滚 | [双上游源码自更新](SOURCE_UPDATES_CN.md) |
| 配置职责、日志、旧数据导入、离线备份恢复 | [运维与数据迁移](OPERATIONS_CN.md) |
| 克隆、构建、测试、更新锁定上游、Docker | [开发与构建](DEVELOPMENT_CN.md) |
| 查看功能边界和实施记录 | [实施状态](IMPLEMENTATION_STATUS_CN.md) |
| 判断是否可以正式发布 | [正式发行检查清单](RELEASE_CHECKLIST_CN.md) |
| 了解集成方案和历史决策 | [项目计划](PROJECT_PLAN_CN.md)、[页面设计约束](UI_DESIGN_CN.md) |
| 了解上游许可与分发限制 | [第三方声明](../THIRD_PARTY_NOTICES.md) |

## 推荐的更新方式

独立 Portable 实例使用：

```powershell
.\cliproxyapi-plus.exe --config config.yaml --data-dir data --web-updates --source-updates
```

登录“系统信息 → 程序更新”，检查、确认并构建目标上游组合。完整包内置 SDK；仍需要访问 GitHub 和 Go/npm 依赖。**不需要额外发行服务或索引。** 回滚恢复程序及对应数据快照，不会合并快照后的写入。完整约束见[源码自更新文档](SOURCE_UPDATES_CN.md)。

## 高级兼容模式，不是必选安装步骤

仅在操作者已经维护可信的整合产品发行源时阅读以下文档，不要将其与默认双上游源码更新混为一谈：

- [网页整包索引更新](WEB_UPDATES_CN.md)：操作者索引、安装事务和离线故障恢复。
- [本机 HTTP 索引更新](LOCAL_WEB_UPDATES_CN.md)：显式允许回环 HTTP 的例外规则。
- [独立本地发行服务](RELEASE_SERVER_CN.md)：可选的只读发行源程序，不是产品所必需的第二服务。

这些文档包含历史版本示例；其中的包/版本号需要替换为已经验证的实际产物。正式版本号、校验和或 Git 推送成功都不能代替二进制再分发审查及生产部署验收。

## 文档和安装包的关系

根目录 `README.md` 与运维、更新、验收等文档随当前打包脚本分发；本索引、完整使用指南及开发指南目前作为仓库文档维护。已解压旧包里的文档不会因 Git 推送自动变化。源码配方记录包含产品文档的摘要，修改后应重新构建匹配产物，不能沿用旧包的验收回执。
