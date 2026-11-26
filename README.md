<p align="center">
  <img src="web/public/favicon.svg" width="72" alt="nekoport" />
</p>

<h1 align="center">nekoport</h1>

<p align="center">面向个人的多邮箱客户端，用一个 Docker 服务管理收发邮件。</p>

<p align="center">
  <a href="https://github.com/a1zaza/nekoport/actions/workflows/publish-image.yml"><img src="https://github.com/a1zaza/nekoport/actions/workflows/publish-image.yml/badge.svg" alt="镜像构建状态" /></a>
  <img src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white" alt="Go 1.26" />
  <img src="https://img.shields.io/badge/React-19-149ECA?logo=react&logoColor=white" alt="React 19" />
  <img src="https://img.shields.io/badge/Docker-amd64%20%7C%20arm64-2496ED?logo=docker&logoColor=white" alt="Docker amd64 / arm64" />
</p>

<p align="center">
  <a href="#功能">功能</a> ·
  <a href="#快速开始">快速开始</a> ·
  <a href="#配置">配置</a> ·
  <a href="#本地开发">本地开发</a> ·
  <a href="https://github.com/a1zaza/nekoport/issues">问题反馈</a>
</p>

Go 后端 + React 中文界面，前端资源嵌入单个 Go 程序。采用深灰蓝色三栏布局，支持聚合收件、HTML 阅读和纯文本写信，使用 JSON / JSONL 保存状态，无需数据库。

## 功能

|            | 支持内容                                                                                       |
| ---------- | ---------------------------------------------------------------------------------------------- |
| 多邮箱     | 手动配置 IMAP / SMTP，聚合收件箱、文件夹、星标、筛选与本地搜索                                 |
| 邮件阅读   | 保留 HTML 邮件自身配色与宽度，外层布局居中；远程图片按单封授权，支持纯文本、附件及原始邮件下载 |
| 写信与发送 | 纯文本写信、回复、转发、附件、自动保存草稿，以及持久发件箱和发送结果确认                       |
| 历史与对齐 | 全历史邮件列表导入、定时核对标记与删除，任务支持进度、取消和继续                               |
| 同步日志   | 增量同步与完整对齐分类，每邮箱每类保留最近 5 次已结束任务，记录对比、新增、更新及删除          |
| 部署与存储 | Go 内嵌前端、单服务 Docker、非 root 运行；JSON 检查点与 JSONL 事务日志，启动恢复内存索引       |

当前面向个人、单用户、多邮箱、单实例使用。支持密码或应用密码认证的 IMAP / SMTP；暂不支持 OAuth、POP3、富文本编辑、桌面端和多用户。

## 快速开始

需要 Docker Compose，以及容器运行用户 `1000:1000` 可读写的数据目录。

### 1. 获取项目与镜像

```bash
git clone https://github.com/a1zaza/nekoport.git
cd nekoport

mkdir -p data
chmod 700 data

docker compose pull
```

默认镜像为 `ghcr.io/a1zaza/nekoport:latest`。镜像需要先由 [发布工作流](https://github.com/a1zaza/nekoport/actions/workflows/publish-image.yml) 成功发布；若包为私有，请先完成 GHCR 登录。挂载目录的所有权需与容器运行用户匹配。

<details>
<summary>尚未发布镜像，或希望从源码构建</summary>

使用下面的命令替代 `docker compose pull`，后续步骤在同一终端执行：

```bash
export NEKOPORT_IMAGE=nekoport:local
docker compose build
```

要长期使用本地镜像，可将 `NEKOPORT_IMAGE=nekoport:local` 写入本地 `.env`，供后续 Compose 命令使用。

</details>

### 2. 启动并查看初始账号

```bash
docker compose up -d
docker compose logs nekoport
```

全新数据目录首次启动时，程序会自动生成 `master.key`（权限 `0600`），创建用户名 **`admin`** 和一个安全随机密码，并仅在该次启动的控制台输出登录信息。应用不生成明文账号密码文件，只持久化用户名和密码哈希。

打开 **<http://127.0.0.1:8080>**，使用控制台中的账号密码登录，建议随后在“设置与同步 → 账户安全”修改密码。重启沿用已有账号，不重新生成或输出密码；已有数据或账号配置异常时也不会自动重置。

登录后添加邮箱，填写 IMAP / SMTP 主机、端口、TLS 和登录信息。容器中的 `localhost` 指容器本身，请使用容器实际可达的邮件服务器地址。

<details>
<summary>希望自行设置首次登录用户名和密码（可选）</summary>

在首次启动服务之前执行以下命令，将 `myname` 替换为你的用户名，按提示设置密码：

```bash
docker compose run --rm --no-deps nekoport init \
  -data /var/lib/nekoport \
  -username myname
```

手动初始化后，服务沿用你设置的账号，控制台不会另行生成默认密码。平台登录账号与邮箱服务商账号分别配置；`init` 不覆盖已有账号。

</details>

## 配置

Compose 默认只将 HTTP 端口映射到宿主机回环地址。以下配置可通过环境变量或项目根目录的 `.env` 设置：

| 变量             | 默认值                           | 说明                               |
| ---------------- | -------------------------------- | ---------------------------------- |
| `NEKOPORT_IMAGE` | `ghcr.io/a1zaza/nekoport:latest` | 使用的镜像，也可覆盖为本地构建标签 |
| `NEKOPORT_PORT`  | `8080`                           | 宿主机 HTTP 端口                   |

示例：

```dotenv
NEKOPORT_PORT=8080
NEKOPORT_IMAGE=ghcr.io/a1zaza/nekoport:latest
```

### 数据与主密钥

只需挂载一个目录：`./data:/var/lib/nekoport`。

| 宿主路径            | 内容                                               |
| ------------------- | -------------------------------------------------- |
| `./data`            | 邮件索引、状态、正文及附件缓存、草稿和任务日志     |
| `./data/master.key` | 解密邮箱凭据的主密钥，缺失且没有已存凭据时自动生成 |

`init` 和 `serve` 默认使用各自 `-data` 目录下的 `master.key`，也可用 `-master-key-file` 显式指定其他位置。密钥为空、无效或丢失但已有加密凭据时会明确报错，不会用新密钥替换。

**请完整备份 `./data`，其中包含主密钥。** 数据目录及备份都应限制为个人访问；丢失密钥后无法解密原邮箱凭据，替换镜像或重建容器不应重新生成已有密钥。

<details>
<summary>从旧的 data + secrets 双挂载部署迁移</summary>

1. 停止旧容器，保留原数据和密钥备份。
2. 将原 `./secrets/master.key` 原样复制到 `./data/master.key`，权限设为 `0600`，所有者需匹配容器运行用户。若目标文件已存在，先确认是否为同一密钥，禁止直接覆盖。
3. 更新 Compose，保留数据目录挂载，移除原独立密钥挂载，然后启动新镜像。

有已存邮箱凭据时必须使用原密钥；不能通过删除密钥让应用重新生成来完成迁移。

</details>

升级前正常停止服务，完整备份数据目录（含 `master.key`）和旧镜像 digest。不要在运行时手工修改 JSON / JSONL，或只复制部分文件；恢复规则见 [开发与维护文档](docs/development.md#数据布局与恢复)。

## 同步与发送说明

- **实时增量**通过 IMAP IDLE 监听各个可选择文件夹，收到新邮件通知后获取新增 UID，不再定时轮询；普通“同步”按钮仍可手动获取新增邮件。连接中断后自动退避重连，并只补偿受影响目录。
- **移动与标记**按目录或指定 UID 更新，不触发全账号完整对齐；移动、归档同时核对来源和目标。本地移动的补偿会持久化，不依赖服务器一定推送事件。
- **完整对齐**默认每 15 分钟核对服务器状态，可在“同步设置”调整周期或点击“立即对齐”。远端删除、归档移出和文件夹消失，在成功对齐并确认后才清理本地索引。
- **监听状态**在“同步设置”查看连接数量、重连提示与错误。不支持 IDLE 的服务器不会退回增量轮询，可使用手动同步及定时完整对齐。每账号默认 IMAP 连接预算为 10，可在编辑连接中设置 4–50，预留 3 个用于操作与正文；未实时监听目录会明确展示。服务器连接配额会影响覆盖；新建目录等未被服务器通知的变化仍由完整对齐发现。
- **全历史同步**覆盖所有可选择目录的邮件列表，正文和附件按需加载，不等同于离线备份。后台任务由 Go 执行，不依赖网页打开。
- **任务日志**记录目录快照和变化统计；每任务保留最近 200 条过程明细，累计统计完整保留，进行中与暂停任务额外保留。
- **已发送副本**默认经 IMAP 保存，随后主动更新对应目录，即使没有 IDLE 通知也能刷新本地列表。选择“服务器会自动保存已发送邮件”时客户端不重复 APPEND，界面保留未核实提示。
- **发送结果**中的“已发送”表示 SMTP 已接受，不保证最终投递。提交结果不确定时需人工核对，应用不会自动重发；归档失败也不会重复发送 SMTP。
- **安全移动**要求服务器支持 UID MOVE，不降级为可能影响其他邮件的全局 EXPUNGE。读取同步本身不会删除服务器邮件。
- **容量**上限为 20 万封邮件元数据，超限明确报错；正文缓存预算单独管理，草稿和待发送快照受保护。

在“设置与同步 → 账户安全”可修改平台用户名和密码。保存成功后所有会话退出，使用新信息重新登录；邮箱服务商密码仍由服务商管理。

## 本地开发

工具链：**Go 1.26、Node.js 24**。Go module 为 `github.com/a1zaza/nekoport`，前端源码位于 `web/`。

开发前端由 Vite 提供，监听 `0.0.0.0:5173`，将 API 代理至 `127.0.0.1:8080`。初始化、前端构建、Go 编译及启动步骤见 [开发文档](docs/development.md)。

常用检查：

```bash
# 项目根目录
go test -race ./...
go vet ./...

# 前端
cd web
npm ci --ignore-scripts
npm run typecheck
npm test
npm run format:check
```

发布构建先生成前端，再编译 Go。Dockerfile 已包含完整构建流程；GitHub Actions 在 `main` 推送或手动触发时构建原生 `amd64` / `arm64` 镜像，按 digest 合并发布 `latest`。

## 文档

- [开发、测试、部署与恢复](docs/development.md)
- [邮件客户端设计](docs/mail-client-design.md)
- [账户安全与历史同步方案](docs/account-history-sync-plan.md)
- [实现范围与验证记录](docs/implementation-status.md)
- [变更记录](docs/changelogs/README.md)
- [字体资源与授权](web/src/assets/fonts/README.md)

问题与建议可在 [Issues](https://github.com/a1zaza/nekoport/issues) 提交，请附版本、复现步骤及脱敏日志。
