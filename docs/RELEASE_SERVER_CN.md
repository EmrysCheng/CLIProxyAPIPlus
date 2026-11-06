> 现行默认方案已调整：0.4.0 起直接在页面更新两个基础项目，无须本地发行服务或 index.json。请先阅读 [SOURCE_UPDATES_CN.md](SOURCE_UPDATES_CN.md)。本文仅保留为可选历史兼容发行源说明，不是新功能的必选部署步骤。

# CLIProxyAPIPlus 本地发行服务便携包

适用版本：0.4.0。本文描述可从仓库构建的独立发行服务，本次公开 Release 不附带该二进制包。该服务是独立的本机只读发行源，不是 CLIProxyAPIPlus 主程序，也不会替你执行升级。

## 1. 包含什么

```text
CLIProxyAPIPlus_ReleaseServer_<版本>_<系统>_<架构>_portable.zip / .tar.gz
  cliproxyapi-plus-release-server.exe    # Linux 无 .exe 后缀
  release-server.json
  start.cmd                             # Linux 为可执行 start.sh
  README.md
  build-info.json
  checksums.txt
  THIRD_PARTY_NOTICES.md
  licenses/Go-LICENSE.txt
  releases/
    index.json
    CLIProxyAPIPlus_<版本>_<系统>_<架构>_portable.zip / .tar.gz
```

`releases` 中包含打包时已构建并校验的本版产品平台包：本地三平台一起构建时会带上三个包；CI 单平台构建只带对应产品包。**使用者不需要安装 Python、Node、Go 或配置 pyenv**。包可以复制到其他可信安装目录后使用，不依赖开发工作区的绝对路径。

发行服务包名称中有 `ReleaseServer`，不能当作应用升级包；主程序仍安装普通 `CLIProxyAPIPlus_<版本>_...` 整合包。内层产品包各自保留 SBOM、许可和上游声明；服务本身不增加第三方运行时依赖。

## 2. Windows 解压启动

1. 将 **ReleaseServer Windows amd64 便携包**解压到一个新的、可信账户可写的目录，例如 `D:\Deploy\CLIProxyAPIPlus-ReleaseServer`。不要覆盖正在运行的 EXE，不要解压到主程序的 data 目录。
2. 双击 `start.cmd`，或执行：

```powershell
& "D:\Deploy\CLIProxyAPIPlus-ReleaseServer\cliproxyapi-plus-release-server.exe" `
  --config "D:\Deploy\CLIProxyAPIPlus-ReleaseServer\release-server.json"
```

不传 `--config` 时，默认从**可执行文件旁边**读取 `release-server.json`，不是当前工作目录。启动脚本使用自己的目录，目录名带空格也可用。

3. 保持窗口运行，看到以下索引地址：

```text
http://127.0.0.1:26973/index.json
```

4. 用另一个 PowerShell 窗口验证：

```powershell
Invoke-RestMethod "http://127.0.0.1:26973/healthz"
Invoke-RestMethod "http://127.0.0.1:26973/index.json" | ConvertTo-Json -Depth 8
```

5. Ctrl+C 正常停止。请求日志不刷控制台，见 `logs/release-server.log`；默认 10 MiB 轮转，保留 `.1`、`.2`、`.3` 三个备份。日志打不开会启动失败。

如果此前正在运行 Python 文件服务器并占用 26973，请先在**那个窗口**正常退出它；不要猜 PID 或停止不明进程。端口被占用时，本服务明确报错，不会抢占端口。

## 3. Linux 解压启动

```sh
# 按实际版本/架构选择 .tar.gz；解压到一个新目录。
./start.sh
# 或使用绝对安装路径，工作目录可以不同：
/opt/cliproxyapi-plus-release-server/cliproxyapi-plus-release-server --config /opt/cliproxyapi-plus-release-server/release-server.json
```

归档中服务程序与 `start.sh` 为 0755。Linux 原生验收应在对应平台运行；Windows 交叉构建不等同于 Linux 实跑。

## 4. 配置文件

```json
{
  "schema_version": 1,
  "listen": "127.0.0.1:26973",
  "release_dir": "releases",
  "log_file": "logs/release-server.log",
  "log_max_size_mb": 10
}
```

- `listen`：只接受回环 IP 字面量和固定端口 1..65535，如 `127.0.0.1:26973`、`[::1]:26973`。不支持 `localhost`、局域网地址、通配地址或公网监听。
- `release_dir`：索引和安装包目录，相对路径从**配置文件目录**解析。
- `log_file`：日志路径，同样相对配置文件目录；建议放在发行目录外。
- `log_max_size_mb`：1..128，默认 10；保留三个轮转备份。
- 不认识的配置字段会报错，不静默忽略。

**换端口/回环 IP 时，索引 `release.assets[].url` 的主机/端口也必须同步修改**，然后同步修改应用的 `--release-index`。服务会拒绝指向其他源、带 query/credentials、路径不匹配或损坏的索引。推荐在构建时使用 `--release-listen` 一次生成一致配置，而不是只修改 listen。

只校验、不监听：

```powershell
& "D:\Deploy\CLIProxyAPIPlus-ReleaseServer\cliproxyapi-plus-release-server.exe" --check
& "D:\Deploy\CLIProxyAPIPlus-ReleaseServer\cliproxyapi-plus-release-server.exe" --version
```

启动和 `--check` 会验证索引、受允许的平台包名、完整包 SHA256。运行中每次请求重新读取索引，发布新版时不需要重新编译服务。

## 5. 配置应用并在页面操作

保持原配置/data 路径，在独立 Portable 的原应用启动命令中增加：

```text
--web-updates --allow-local-release-http --release-index "http://127.0.0.1:26973/index.json"
```

- 服务与应用必须运行在**同一机器**，127.0.0.1 指服务进程所在机器，不是远程浏览器的机器。
- 应用仍使用原来的管理业务端口（例如 8317）；26973 只是发行源，不是第二个代理/管理端口。
- 以 Manager 管理员身份登录更新页，检查更新、输入目标版本确认后升级；成功记录中可回滚，并须确认程序/配置/数据一起恢复。
- 只有索引中的真实版本高于当前版本且包/源码身份匹配时才提示升级；相同版本不提示，不伪造版本用于演示。
- 升级权限、自包含 data/认证路径、私有快照和禁止外部自动拉起等限制不变。停止发行服务不会停止应用；快照回滚不依赖它在线。
- 早期不支持本机 HTTP 开关的实例，第一次需停机备份后迁移到经过验证的完整安装包。不要重新 init 已有数据。

完整升级/回滚限制见产品包中的 `docs/LOCAL_WEB_UPDATES_CN.md`。

## 6. 后续如何替换本机发行内容

**使用者不需要 Python：** 获得经过验证的新 ReleaseServer 便携包后，解压到新目录，停止旧发行服务，再启动新包，应用仍指向同一个索引 URL；也可停止服务后将新版准备好的 `releases` 目录整体放到原 `release_dir`，再 `--check`、启动。应用本身此时继续运行，直到管理员在页面确认升级。

开发者可在源码工作区构建/发布：

```powershell
# 先构建并验证需要分发的主程序平台包；多平台要基于同一产品源码。
python scripts/project.py build --target windows/amd64
python scripts/project.py build --skip-web --target linux/amd64
python scripts/project.py build --skip-web --target linux/arm64

# 再打包独立发行服务（会内含当前已构建的产品包和索引）。
python scripts/project.py build-release-server --target windows/amd64
python scripts/project.py build-release-server --target linux/amd64
python scripts/project.py build-release-server --target linux/arm64

# 自选本机端口：生成配置、索引中的 URL 会一起改变。
python scripts/project.py build-release-server --target windows/amd64 --release-listen 127.0.0.1:26974
```

上面 Python 是**开发构建工具**，不是发行服务运行依赖。开发环境应配置 Python 3.11+、Go 和前端构建工具，或使用其实际绝对路径。`--skip-web` 仅在本版前端已经成功构建并验证时使用。

构建命令拒绝包摘要错误、混合源码摘要或已过期的产品包。不会直接跟随上游 main，不自动签名/上传、不停止任何已有服务，不复用旧目录内的运行日志打包。

需要在线刷新发行目录的发布者仍可用开发工具 `scripts/release_index.py --copy-assets --replace`，先准备完整新包再原子替换索引；相同包名的不同内容拒绝覆盖。**不要原地编辑正在被请求的半份 index.json，不要编造版本号或摘要。**

## 7. 安全与范围

仅提供 GET/HEAD：`/index.json`、索引中明确列出的整合包，以及 `/healthz`。不列目录，不暴露配置、日志、README、data 或私有密钥，不提供上传、执行命令、安装、回滚或 Manager 管理接口。拒绝与配置监听地址不一致的 Host；文件访问被限制在发行目录中，拒绝链接/路径逃逸。

这是本机可信账户维护的 HTTP 发行源，不提供网络加密/发行签名。发布目录与其父目录必须由可信本机账户维护；不能把它改造成公网或局域网发布服务。跨机器部署仍使用原可信 HTTPS 发行源。主程序和本服务均按实际构建版本报告发行通道；本次 0.4.0 是正式源码发行，不附二进制。其他生产门禁不会因版本正式化或新增打包自动关闭。
