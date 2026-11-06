# 开发、构建与上游维护

适用版本：`0.4.1`。本指南描述仓库提供的构建与验证入口，不代表所有平台、供应商或生产环境已通过验收。使用部署看[用户指南](USER_GUIDE_CN.md)，交付门禁看[发行检查清单](RELEASE_CHECKLIST_CN.md)。

本次 [v0.4.1](releases/v0.4.1.md) 增加三个原生目标的完整 Portable 发行链路；工程验证不替代具体业务、安全、许可和生产部署签收。

## 1. 仓库结构与修改边界

```text
cmd/cliproxyapi-plus/      整合程序入口与离线维护入口
internal/                 路由集成、内部传输、产品元数据、更新事务等
patches/                  对两个上游实施的严格适配与新增文件
scripts/                  快照补齐、构建、候选维护、备份和验收工具
tests/                    Python 工具链回归测试
docs/                     使用、运维、实施及验收文档
upstream.lock.json        上游仓库、精确提交与原始树摘要
upstream/                 本地补齐的不可变原始快照，不提交公开 Git
.build/                   隔离构建副本、缓存、候选及私有报告
dist/                     本地构建产物，不提交公开 Git
```

开始修改前阅读 [AGENTS.md](../AGENTS.md)。调整集成架构前还须阅读[项目计划](PROJECT_PLAN_CN.md)和[实施状态](IMPLEMENTATION_STATUS_CN.md)。

- 不编辑 `upstream/`。上游适配写入 `patches/<project>/`，由 `scripts/project.py` 准备隔离副本。
- `.build/sources/` 和前端工作目录都是生成物；直接修改不会成为可重放实现。
- 不绕过 Manager 外部管理鉴权，不替换全局 HTTP Transport；内部调用使用注入的专用 transport。
- Go 修改后运行 `gofmt`、产品测试和二进制构建，并补充受影响的回归。
- 不将未验收安装包描述为已完成生产准入，不把“可编译”当作完成生产验收。

## 2. 环境要求

| 工具 | 当前开发基线 |
| --- | --- |
| Python | 3.12+，建议与 CI 一致使用 3.13 |
| Go | 1.26.10 |
| Node.js | 24 系列，并满足锁定上游 engines |
| npm | 与 Node/锁文件兼容的版本；完整 Portable 包有固定版本 |
| Git | 克隆和维护本产品仓库；固定上游补齐脚本本身不要求 Git |
| CGO / C 工具链 | 仅 Full、race 等需要的平台原生验证 |

开发构建会访问 npm、Go modules、GitHub 和打包 SDK 来源。安装包内置 SDK 是终端用户功能，不代表初次源码构建不需引导环境或网络。`npm ci --ignore-scripts` 按锁文件安装依赖，不通过执行任意安装脚本修复环境。

## 3. 首次克隆与快照补齐

```powershell
git clone https://github.com/EmrysCheng/CLIProxyAPIPlus.git
cd CLIProxyAPIPlus
python scripts/hydrate_upstreams.py
python scripts/project.py prepare
python scripts/project.py build
```

公开 Git 仅保存产品代码、补丁和锁定清单，原始快照从两个指定官方仓库按**完整提交**获取。脚本核对树摘要、模块身份及 LICENSE，不查询移动标签、不改 lock、不覆盖已有树。已有目录内容不符会明确失败，先检查来源和本地修改，不应直接改摘要使其通过。

在已有可信快照的离线环境，仅验证：

```powershell
python scripts/hydrate_upstreams.py --offline
```

缺失快照会失败；此命令不是下载依赖、安装 SDK 或完成整个离线构建的命令。

当前锁定基线为：

| 项目 | 锁定版本 | 完整提交 |
| --- | --- | --- |
| CLIProxyAPI | `v8.0.53` | `01b24f1f95dc9b309765212c52b25b25e005ff01` |
| CPA-Manager-Plus | `v1.14.10` | `a67ed8834a914ab24c36d2490fa6bb6d4166724c` |

以 [upstream.lock.json](../upstream.lock.json) 为准；此表不是“此刻上游最新”的声明。

### 自定义开发工具

```powershell
python scripts/project.py build --go "D:\Tools\Go\bin\go.exe" --node "D:\Tools\Node\node.exe" --npm-cli "D:\Tools\Node\node_modules\npm\bin\npm-cli.js"
```

路径替换为自己的可信工具。打包阶段默认下载、核对并内置目标平台 SDK；已有同平台且完全匹配的工具包时，可加 `--tool-bundle <可信build-tools绝对路径>` 复用，仍会校验。

## 4. 构建产物与身份

默认 `build` 执行快照准备、真实前端构建和校验、Python/产品 Go 测试、二进制编译、许可资料汇集、SDK 打包及源码配方生成。它不是完整平台/生产验收的替代物。

```powershell
python scripts/project.py build --target windows/amd64
python scripts/project.py build --target linux/amd64
python scripts/project.py build --target linux/arm64
```

已有当前匹配的真实前端可加 `--skip-web`，但仍验证前端嵌入摘要，不能用占位页面绕过。交叉构建 Linux 产物不意味着在 Windows 执行了 Linux 原生测试。

```sh
# 在具备对应原生 CGO 工具链的 Linux 环境执行并验收
python scripts/project.py build --variant full --target linux/amd64
```

常见产物：

```text
dist/<os>-<arch>-<variant>/
  cliproxyapi-plus[.exe]
  build-info.json / checksums.txt
  sbom.spdx.json / licenses-index.json / compliance-report.json
  licenses/ / tools/ / docs/
  build-tools/ / update-tools.json / updater-source/
dist/CLIProxyAPIPlus_<version>_<os>_<arch>_<variant>.zip 或 .tar.gz
```

`build-info.json` 记录产品源码摘要、lock 摘要、双上游版本/提交、平台和二进制摘要。`updater-source/source-kit.json` 对产品文件及原始快照建立清单；主程序和源码配方必须匹配。

**不要只移动 EXE、拼接不同构建的配方、手工改 SHA 或沿用旧验证报告。** `docs/` 和 `README.md` 也参与当前产品源码摘要；修改后旧产物不会变成“新源码验收通过”。`README.md` 是仓库入口，当前源码配方白名单与普通包文档清单见脚本，不能假设根目录每个文件都随包复制。

`--skip-sbom` 仅用于诊断，不能因此宣称满足发行合规要求。生成 SPDX/许可索引不等于声明完整、无漏洞或获得法律审批。

## 5. 验证入口

按顺序先准备并构建当前原生平台的完整 Portable 包，再运行需要实际产物的测试：

```powershell
python -m unittest discover -s tests -p "test_*.py" -v
python scripts/project.py test
python scripts/project.py build
python scripts/project.py test-e2e
python scripts/project.py test-update
python scripts/project.py test-source
python scripts/project.py test-source-build
python scripts/project.py test-web
python scripts/regression.py
python scripts/smoke_binary.py
python scripts/smoke_maintenance.py
```

| 入口 | 主要范围 |
| --- | --- |
| Python unittest | 构建/归档路径、快照补齐、严格适配、工具平台和相关脚本回归 |
| `project.py test` | 准备副本后的 Python 与产品 Go 测试 |
| `project.py test-e2e` | 单端口及管理/模型协议集成验证 |
| `project.py test-update` | 独立临时实例网页安装、成对回滚及失败恢复 |
| `project.py test-source` | 随包源码配方跨语言校验、内置 SDK 就绪性 |
| `project.py test-source-build` | 用实际随包配方/工具执行基线源码构建流水线，要求新鲜成功回执 |
| `project.py test-web` | 前端 lint、完整 Vitest 及工作流检查 |
| `regression.py` | 适配范围回归及保留的原始独立入口编译 |
| 二进制/维护 smoke | 实际程序 API、日志、时区及维护操作烟测 |

额外在原生 Linux/相应工具链上运行：

```sh
python scripts/regression.py --race
python scripts/regression.py --all-upstream
```

失败、跳过或超时不等于通过，也不能在未见 race 报告时把一般超时写成“检测到数据竞争”。具体结果以对应提交、源码身份及新鲜回执为准。

测试使用隔离实例；不要将真实运行 data、生产凭据或真实备份当测试夹具。执行上游代码前审查信任边界，构建目录不是 OS 沙箱。

## 6. 持续更新两个上游

### 6.1 源码维护流程

```powershell
# 只读检查：stable 为正式发布；edge 为上游 main 当前提交
python scripts/project.py check-updates --channel stable

# 创建隔离候选，记录返回的实际 candidate 目录
python scripts/upstreams.py stage --project all --channel stable

# 将占位符替换为 stage 返回的目录
python scripts/upstreams.py validate --candidate <candidate-directory>

# 审阅源码差异、测试和回执后，显式晋级工作区基线
python scripts/upstreams.py promote --candidate <candidate-directory>
```

- `--project` 可选 `all`、`cliproxyapi`、`cpa-manager-plus`；单侧更新时另一项目固定。
- `stage --project cliproxyapi --ref <tag或提交>` 可显式固定单个项目的引用（不能与 `--project all` 同用）；人工审阅后以记录的完整提交为准。
- 页面只跟踪正式版；开发 `edge` 候选不应无审批进入运行实例。
- 补丁前像变化立即失败，不模糊匹配、不原地改 `upstream/`。需要新的严格适配时先修改 `patches/` 再重建候选。
- 产品/补丁/文档、基线 lock 或候选树在验证后发生变化，回执失效；不能手改 JSON 冒充重新验证。
- `promote` 只更新源码快照与 lock，不安装运行程序、不推送、不发布。晋级后重新构建并完成验收。

晋级中断由可信私有 journal 恢复：

```powershell
python scripts/upstreams.py recover --journal <promotion-journal>
```

不要对未知 journal 执行恢复。源码晋级与用户页面安装事务是两种操作，后者见[源码自更新](SOURCE_UPDATES_CN.md)。

### 6.2 CI 与正式发布

- `verify.yml` 验证三平台 Portable 与 Linux Full 的配置范围；具体是否通过应查看当前提交的实际工作流。
- `upstream-candidate.yml` 定时或手动建立、验证候选，不自动 promote、提交或发布。
- 普通验证 / 候选 artifact 仍只允许构建身份、校验和及候选元数据，不上传完整安装包或宽泛日志。
- `portable-release.yml` 仅允许从本仓库 `master` 手动触发。先提交全部代码/文档，等待该提交的 `verify.yml` 全部通过，再创建不可变正式标签（与 `VERSION` 一致），在 Actions 的 **Publish native Portable packages** 选择 `master`，填写如 `v0.4.1` 后运行。标签、运行提交和 master 必须一致。
- 三个平台原生构建、SDK/配方验证、程序及维护 smoke 全部通过后，只有独立 publish job 获得 `contents: write`；上游 Go/npm/测试仅用只读权限。发布者重新逐文件校验归档，不执行包内代码。
- 三个平台包、SHA256 和脱敏回执先传入 draft，附件齐全且摘要一致后才公开。同名同摘要跳过，不同摘要或已公开 Release 缺附件则拒绝覆盖；失败时在同一次 run 使用 **Re-run failed jobs**，不重新构建/覆盖已上传的不同字节资产。
- 正式目标包括 Windows amd64、Linux amd64、Linux arm64；增加新系统必须同时增加可信 SDK 锁、原生 runner、验证与发行资产约束，不能只改文件名。
- `internal/sourceupdate/portable-notices.lock.json` 按目标平台和组件身份固定依赖声明及 notice 的 required/optional 路径和内容摘要，要求 R ⊆ S ⊆ R ∪ O；不能删掉 required，也不能把另一组件的许可证挪用过来。运行程序链接的 Go 模块和非可选的前端依赖保留必需 notice；测试专用 Go 模块、可选原生 npm 包及其平台差异只按锁中显式 optional 清单接受。归档内 `licenses-index.json` 不是任意文件的信任来源。新增平台或更新依赖若出现未审阅的 notice 摘要，发行失败关闭；需对照固定依赖官方内容审阅后更新锁，不能将私有日志改名放入 licenses 或直接放宽白名单。
- 不向上游候选测试或 PR 工作流提供生产凭据/发布 token；不使用不可信源码配合 `pull_request_target` 获得权限。
- 第三方原始源码、OAuth 常量、依赖许可/缺失 notice/漏洞与整包再分发仍须单独审查。Git 推送成功不等于得到正式发布许可。

## 7. Docker / Compose

仓库提供多阶段 Dockerfile 和单服务 Compose，当前镜像名为本地 `cliproxyapi-plus:local`，没有承诺预发布公共镜像。容器部署需要原生环境验证；不能把以下命令视为已执行的证据。

在 Linux 主机、仓库根目录执行：

```sh
mkdir -p runtime
# 绑定目录仅赋予可信部署账户/容器 UID 必要权限；不要 chmod 777
sudo chown 10001:10001 runtime
docker compose build
# 全新 runtime 才初始化；已有数据不要重复执行
docker compose run --rm --no-deps cliproxyapi-plus --init --config /state/config.yaml --data-dir /state/data --listen 0.0.0.0:8317
docker compose up -d
docker compose logs --tail=100 cliproxyapi-plus
```

Compose 默认将宿主 `127.0.0.1:8317` 映射到容器 8317，挂载 `./runtime:/state`。容器内使用 `--allow-remote` 是为了绑定容器网卡，不表示允许裸露公网。当前 Dockerfile 复制完整构建目录，因此镜像也可能包含随包 SDK；业务入口不运行这些工具、不启用页面自替换，不能把镜像称为已裁剪的无 SDK 最小镜像。停止、备份、重建镜像、迁移和回退由部署系统负责。

## 8. 提交与文档维护

1. 保持 UTF-8、LF；不要因行尾转换改变锁定树或使源码身份不一致。
2. 将适配、测试及说明放在可重放的产品路径，不提交缓存或生成目录。
3. 提交前检查 diff、秘密和验收范围；不得 `git add -f upstream`，也不提交 data、认证、备份、私有事务或本机工具配置。
4. 只推送经过审阅的产品变更，不绕过 GitHub secret scanning，也不把原始快照重新编码后移入其他目录。
5. 修改构建协议、产品路径白名单、配方或升级协议时核对 Python/Go 双端契约并补充验证；文档变更不应顺便放宽安全门禁。
6. 提供真实测试结果与未覆盖范围；源码 tag/Release 与二进制再分发、生产部署分别记录验收范围；不得将未完成项写成已通过。

上游归属与许可声明见 [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md)。本指南不替代码所有者选择新增产品代码的许可证。
