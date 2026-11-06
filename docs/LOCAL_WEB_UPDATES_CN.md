> 现行默认方案已调整：0.4.0 起直接在页面更新两个基础项目，无须本地发行服务或 index.json。请先阅读 [SOURCE_UPDATES_CN.md](SOURCE_UPDATES_CN.md)。本文仅保留为可选历史兼容发行源说明，不是新功能的必选部署步骤。

# 本地页面升级：配置、提醒、执行与回滚

适用版本：**0.4.0**。本节为可选兼容部署方案，不代表所有生产部署门禁已签收。所需产品包/发行服务包须自行构建并核验，本次公开 Release 不附带二进制。

## 1. 地址分别代表什么

| 地址/参数 | 用途 |
| --- | --- |
| `http://127.0.0.1:8317/` | 整合程序的管理页面和 API；保留原配置端口 |
| `http://127.0.0.1:26973/index.json` | 本机静态发行源；端口可自行选择，但索引与包必须使用同一端口 |
| `--web-updates` | 允许管理员在页面确认后执行升级/回滚，不会自动安装 |
| `--allow-local-release-http` | 显式允许本机回环 HTTP 发行源，默认关闭 |
| `--release-index` | **整合程序发行索引 JSON 的完整地址**，不是 GitHub 页面或上游版本接口 |

26973 是操作者另行启动的静态文件服务器，不是新增的产品管理监听端口。整合程序仍只有一个业务监听端口。可以完全不连接公网，但安装包必须预先在本机完成构建/验证并放入发行目录。

HTTP 仅接受**回环 IP 字面量**，如 `127.0.0.1`、`::1`。不接受 `localhost`、局域网 IP、`0.0.0.0`、域名、URL 用户名/密码。HTTP 包和任何重定向都必须与索引同协议、同主机、同端口；不要混用 `127.0.0.1` 和 `localhost`。普通 HTTPS 更新仍保持原有策略，不因开启本开关而允许降级。

**127.0.0.1 是运行 cliproxyapi-plus 的机器**，不是打开网页的另一台电脑。静态发行服务应与程序运行在同一机器，并由可信账户维护。不要将配置、data、私钥或 `.updates` 暴露为静态目录。

## 2. 首次安装支持本地 HTTP 的程序

早期不识别本机 HTTP 开关的实例，需先正常退出旧进程、备份原程序/配置/data，再将已核验的 0.4.0 完整包解压至新目录，以原配置和数据路径启动；不要只替换 EXE 或混用源码配方。**不要对已有实例重新执行 `--init`**。

以下按源码位于 `D:\Code\Project\AI\CLIProxyAPIPlus`、发行源端口 26973 举例。应用的配置/data 路径改为你实际部署的位置；不要为了照抄例子创建一个空 data。

## 可选：构建并使用独立发行服务

仓库可构建 `CLIProxyAPIPlus_ReleaseServer_<版本>_<平台>_portable` 独立包，包含 EXE/可执行程序、配置、启动脚本、索引和已验证的产品安装包。**使用者无须 Python/Node/Go**：解压后 Windows 运行 `start.cmd`，Linux 运行 `./start.sh`，然后继续本文第 5 节配置应用。详细配置见 [RELEASE_SERVER_CN.md](RELEASE_SERVER_CN.md)。下方第 3/4 节保留为开发者的 Python 发布/临时服务替代方式，不再是使用者必选步骤。

## 3. 生成索引并准备发行目录

发布者需要 Python 3.11+；安装后的产品和升级辅助进程不依赖 Python/Node。Windows 可将下面 `py -3` 换成已安装 Python 的绝对路径。

先按项目构建流程生成并验证目标版本 Portable 发行包与 `.sha256`。然后在 PowerShell 执行：

```powershell
Set-Location "D:\Code\Project\AI\CLIProxyAPIPlus"
py -3 "D:\Code\Project\AI\CLIProxyAPIPlus\scripts\release_index.py" `
  --base-url "http://127.0.0.1:26973" `
  --allow-local-release-http `
  --copy-assets `
  --output "D:\Code\Project\AI\CLIProxyAPIPlus\.build\local-releases\index.json"
```

- 默认读取当前 `VERSION`，扫描 `dist` 中同版本的平台包，核对 ZIP/tar.gz 旁的 SHA256，并从包内 `build-info.json` 读取真实 `product_source_sha256`。**不需要伪造 Git commit，也无需为本地使用先初始化 Git**。
- 同一发行的所有平台包必须具有同一源码摘要和版本。索引的源码摘要会与候选可执行程序 `--version` 输出匹配；篡改索引版本号不能让旧包冒充新版。
- `--copy-assets` 将校验过的包复制到索引所在目录，不会覆盖同名但内容不同的包。该目录只包含可公开的索引和安装包。
- 已有真实产品 Git commit 时可另加 `--source-commit <真实40位提交>`；包内源码 SHA256 仍会记录和核对。旧的 commit-only HTTPS 索引继续兼容。
- 要发布其他已构建版本，可加 `--version <该包内真实版本>`、`--dist-dir <包所在目录>`。不要编造版本或摘要。
- 更新同一个索引时加 `--replace`，会在包准备完成后原子替换索引；仅生成本地文件，不上传、不签名、不执行升级。

生成后的目录类似：

```text
.build/local-releases/
  index.json
  CLIProxyAPIPlus_0.4.0_windows_amd64_portable.zip
  CLIProxyAPIPlus_0.4.0_linux_amd64_portable.tar.gz
  CLIProxyAPIPlus_0.4.0_linux_arm64_portable.tar.gz
```

只有实际构建的平台包才会出现。Windows 安装只选择匹配 Windows/架构的整合包。

## 4. 启动本机静态发行服务

另开一个 PowerShell 窗口，保持服务运行：

```powershell
py -3 -m http.server 26973 --bind 127.0.0.1 --directory "D:\Code\Project\AI\CLIProxyAPIPlus\.build\local-releases"
```

验证返回的是索引 JSON，而不是 HTML 页面或 404：

```powershell
Invoke-RestMethod "http://127.0.0.1:26973/index.json" | ConvertTo-Json -Depth 8
```

这个服务仅用于可信本机发行，不是生产文件托管方案。停止它后，已安装的应用照常运行，但检查/下载新版本会失败；**回滚使用本机已保存快照，不依赖发行服务在线**。

## 5. 启动应用

保留原配置和 data，给原启动命令增加三个参数：

```powershell
# 在原部署目录运行；config.yaml / data 继续使用该实例原有路径。
cliproxyapi-plus.exe --config config.yaml --data-dir data `
  --web-updates `
  --allow-local-release-http `
  --release-index "http://127.0.0.1:26973/index.json"
```

本机 HTTP 不需要 `--release-ca-file`。私有 HTTPS 源仍可使用该证书参数，不应关闭 TLS 证书验证。

必须使用独立 Portable 实例、固定监听 IP/端口、可写的私有安装/数据目录，且认证目录/TLS 材料位于 data-dir 内。不支持与 Windows Service、systemd、容器或外部自动拉起守护器混用；不可绕过页面的禁用原因。更新开关会随正常升级、失败恢复和手动回滚重启参数一并保留。

## 6. 页面提醒和升级

1. 使用 Manager **管理员凭据**登录原管理页面，打开更新页。API key/core key 不具备升级权限。
2. 后端启动时并每 6 小时自动检查。全局提醒约 60 秒刷新，也可在更新页点击“检查更新”。不会后台自动安装。
3. **只有发行版本严格高于当前版本**才出现可安装更新。相同版本显示已是该发行版本，旧版不能当作升级安装。若当前已是 0.4.0，再发布 0.4.0 不会制造提醒；需等待或构建真实的更新版本。没有配置/检查失败不等于“已经最新”。
4. 输入目标版本确认后点击“升级并重启”。系统下载并校验当前平台整合包、程序身份、源码摘要和更新能力，然后正常排空旧进程、备份程序/配置/数据库/密钥/认证文件、安装并验证新进程健康。
5. 页面会短暂断线，约每 3 秒重连刷新操作状态。不要重复点击，不要手动启动第二个实例。以成功记录和新版本信息为准，断线不代表成功。

## 7. 页面回滚

成功网页升级后，历史记录出现“回滚至旧版本”。输入旧版本并勾选数据快照恢复确认，然后执行。

- 回滚同时恢复旧程序、配置、数据库和认证/密钥，**升级快照之后的逻辑数据不会合并回旧库**；回滚前的数据另行保留供人工核对。
- 只有保存了完整快照、成功记录且与当前可执行程序摘要匹配的升级可以回滚；手动复制安装并不会凭空产生回滚记录。
- 升级/迁移失败会尝试成对自动恢复旧版本与数据；不能确认安全恢复时停在 `recovery_required`，按 `OPERATIONS_CN.md` 的私有 worker 离线恢复流程处理，不能直接删除维护标记。
- 快照位于 `<data-dir>.updates/<operation-id>/`，须保留且限制本机访问，不要把它放在发行服务公开目录内。已有回滚记录不需要重新初始化。

## 8. 常见错误

| 现象 | 处理 |
| --- | --- |
| `flag provided but not defined: -allow-local-release-http` | 正在运行旧程序，先停机备份并换成支持该参数的完整安装包 |
| `release index must be ... HTTPS ...` | 没有加本机 HTTP 开关，或 URL 不是回环 IP；生成器和应用均须显式开启 |
| `Product release index unavailable` | 确认静态服务器在同机运行，端口/路径正确，检查不是仅在远端浏览器机器能访问 |
| `invalid product asset metadata` | 索引包 URL 非同源/不受允许，或包名/摘要有误；重新生成，不手填绕过 |
| `unsupported product release metadata` | 索引 schema/版本/真实源码身份不合法；不要将上游 Release API 当作产品索引 |
| 发行版本等于当前版本，无升级按钮 | 正常；要发布真正的新版本，不要仅改索引中的版本号 |
| 页面提示执行能力禁用 | 查看部署/路径/权限原因，独立 Portable 才支持；不要自动重启抢占维护进程 |
| 新程序不支持本机源或源码身份不匹配 | 下载校验阶段拒绝并保持旧服务运行；换用同产品、正确版本与能力的整合包 |

安全边界：本机 HTTP 没有网络层身份认证/加密，同账户/本机管理员和发布目录属于显式信任边界；包摘要也不能替代发行签名。跨机器或正式部署仍使用可信 HTTPS 发行源。此功能不自动追随两个上游最新提交，源码更新仍走候选适配、测试、构建流程。
