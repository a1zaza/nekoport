# nekoport

项目仓库：[a1zaza/nekoport](https://github.com/a1zaza/nekoport) · 镜像：`ghcr.io/a1zaza/nekoport:latest`

个人使用的邮件客户端。Go 提供邮件协议与服务，React 提供中文界面，前端资源嵌入 Go 程序，最终以单服务 Docker 镜像运行。

- 手动配置多个 IMAP/SMTP 邮箱，无默认邮件商或自动发现。
- 深灰蓝色三栏布局、聚合收件箱、文件夹、星标、筛选和缓存搜索。
- 安全阅读 HTML 邮件，远程图片默认拦截，支持纯文本切换与原始邮件下载。
- 纯文本写信、回复、转发、附件与自动保存草稿。
- 持久发件箱、发送幂等键、结果待确认状态；归档失败不会重发 SMTP。
- JSON 检查点、JSONL 事务日志与启动内存索引，不依赖数据库。

仅支持允许密码/应用密码认证的 IMAP/SMTP 邮箱。没有 OAuth、POP3、富文本编辑、桌面程序、多用户或远程托管功能。后台以一分钟轮询同步，不依赖 IDLE 才能更新。永久清除及不支持 UID MOVE 的服务器移动操作不会冒险降级为全局 EXPUNGE。

## 获取源码

```bash
git clone git@github.com:a1zaza/nekoport.git
cd nekoport
```

Go 模块路径为 `github.com/a1zaza/nekoport`。开发与构建命令从项目根目录执行，前端命令在 `web/` 执行。

## Docker 使用

需要 Docker Compose、可读写的本机数据目录和独立的主密钥文件。运行用户为 UID/GID `1000:1000`；先确认当前用户与挂载权限一致。程序不会提权或自动修改无关宿主文件的所有权。

首次使用按以下顺序执行；这些操作由部署者明确执行：

1. 创建 `data` 和 `secrets` 目录，限制为个人访问，并确保容器运行用户可写。
2. 镜像首次发布后，执行 `docker compose pull`，拉取默认的 `ghcr.io/a1zaza/nekoport:latest`。若尚未发布或希望本地构建，先执行 `export NEKOPORT_IMAGE=nekoport:local`，再执行 `docker compose build`；后续命令在同一终端使用该镜像。
3. 显式生成密钥：`docker run --rm --user 1000:1000 -v "$PWD/secrets:/key-output" "$(docker compose config --images)" keygen -master-key-file /key-output/master.key`。原密钥存在时拒绝覆盖。
4. 交互初始化应用账号：`docker compose run --rm --no-deps nekoport init -data /var/lib/nekoport -username <你的用户名>`。
5. 执行 `docker compose up -d`，访问 `http://127.0.0.1:8080`，登录后手动添加邮箱。

密钥生成命令从 Compose 配置读取当前镜像名，因此 `.env` 与终端中的镜像设置保持一致；使用只挂载输出目录的独立命令，避免 Compose 的只读文件挂载在源文件尚未生成时被自动创建为目录。

不要预先创建一个空密钥文件；先生成真实密钥，再运行 Compose。`keygen` 不连接邮箱。若运行用户权限不匹配，请自行调整宿主目录权限或 Compose 的运行用户设置。

默认只向宿主回环地址映射一个 HTTP 端口。可通过 `NEKOPORT_PORT` 更改宿主端口。Compose 默认镜像为 `ghcr.io/a1zaza/nekoport:latest`，可通过 `NEKOPORT_IMAGE` 覆盖；同时保留源码构建配置。若使用本地镜像部署，后续重启或升级前仍需设置相同的 `NEKOPORT_IMAGE`，也可将该非敏感配置写入本地 `.env`。私有 GHCR 包需要先在部署机器完成镜像仓库登录。

**数据与密钥分别保管**：`./data` 保存邮件状态与对象，`./secrets/master.key` 解密邮箱凭据。丢失密钥将无法恢复原凭据；替换镜像和重建容器都不能重新生成密钥。

容器中的 `localhost` 指容器本身。请填写容器实际可达的邮件服务器地址，不关闭证书校验来绕过连接错误。

## 发送与恢复

“已发送”表示 SMTP 已接受，不保证最终投递。提交阶段中断会进入“结果待确认”，请先在邮箱中核对，再确认已发送或取消任务；应用不会自动重发。取消任务无法撤回已经投递的邮件。

缓存配额限制当前引用的可重取内容。保留检查点、日志与对象回收宽限会产生额外磁盘占用；草稿和待发送快照不会因缓存清理被删除。

升级前正常停止服务，完整备份 `data`，并单独保管主密钥与旧镜像 digest。不要运行时手工改 JSON/JSONL 或仅复制部分文件。恢复检测到中段损坏、缺失序号或不支持的格式时会拒绝启动，原文件保留；见[开发与维护说明](docs/development.md)。

## 文档

- [设计方案](docs/mail-client-design.md)
- [开发、测试、构建与维护](docs/development.md)
- [实现范围和验证记录](docs/implementation-status.md)

[镜像发布工作流](https://github.com/a1zaza/nekoport/actions/workflows/publish-image.yml) 在 `main` 推送或手动触发时构建 `linux/amd64`、`linux/arm64`，合并发布 `ghcr.io/a1zaza/nekoport:latest`。工作流根据 GitHub 仓库路径动态生成镜像地址及源码标签。本地配置更新不会触发发布；镜像是否可拉取及包可见性需在首次工作流成功后核验，不会自动部署。

## 账户安全与历史同步

- 在“设置与同步 → 账户安全”修改本平台用户名或密码。必须验证当前密码；保存后所有会话退出，使用新信息重新登录。此操作不会修改邮箱服务商密码。
- 每个邮箱卡片可启动“同步全部历史”，覆盖所有可选择文件夹的邮件列表。正文和附件仍按需加载，不等同完整离线备份。
- “立即对齐”核对服务器当前状态；定时对齐默认每 15 分钟，快速获取新邮件默认每 60 秒，均可配置，任务由 Go 服务执行，不依赖网页打开。
- 任务支持进度、取消和继续；取消不回滚已经导入的数据。服务器邮件删除只会在成功对账并确认后清理本地索引，读取同步不会自动删除远端邮件。
- 暂停账号时停止后台任务；确定认证失败需更新凭据或手动重试。临时网络失败自动退避。任务可跨服务重启恢复，发送结果不确定的邮件仍需人工核对。
- 本地元数据索引最多 20 万封，达到容量会明确报错；正文缓存预算单独管理。

详细规则与本地测试数据见 [账户安全与历史同步方案](docs/account-history-sync-plan.md)。
