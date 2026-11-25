# 项目概述

- `nekoport` 是个人、单用户、多邮箱、单实例邮件客户端，Go + React；最终交付为 Go 内嵌前端的 Docker 单服务，暂不考虑桌面端或远程托管。
- 不使用 SQLite 或其他嵌入式数据库：JSON 检查点 + JSONL 事务日志，启动恢复必要内存索引，正文和附件按需读取。全历史同步不是离线备份，元数据上限 200000 封，超限必须明确报错。
- 邮箱由用户配置 IMAP/SMTP 主机、端口和 TLS，不预置邮件商；共用一组登录信息，编辑密码留空沿用已保存的接收密码，不在打开界面时改写凭据。写信只用纯文本，支持 HTML 阅读。
- 已获授权的实现工作可继续，未明确细节选择稳妥方案；新请求若仅要求设计方案，先写入 `docs/`，不据此直接实施。
- 代码仓库为 `https://github.com/a1zaza/nekoport`（SSH：`git@github.com:a1zaza/nekoport.git`），镜像发布目标为 `ghcr.io/a1zaza/nekoport:latest`；地址已配置不代表镜像已发布。
- 设计与状态：[总体设计](docs/mail-client-design.md)、[账户与同步方案](docs/account-history-sync-plan.md)、[实现状态](docs/implementation-status.md)。

# 开发环境

- 工具链：Go 1.26.5、Node.js 24.18.0、npm 12.2.2；Go module 为 `github.com/a1zaza/nekoport`，前端在 `web/`，依赖由 go.sum/package-lock.json 锁定。
- 未明确要求不启动开发环境、执行 Docker build 或自动运行构建任务；启动前检查端口，不擅自终止其他服务。终止进程前先通过 netstat 核对 PID。
- 开发前端监听 `0.0.0.0:5173`，通过 Vite 代理访问后端 `127.0.0.1:8080`。后端命令：

  ```bash
  ./bin/nekoport serve -data .data -listen 127.0.0.1:8080 -master-key-file .data/master.key -origin http://127.0.0.1:5173,http://localhost:5173,http://172.20.4.2:5173
  ```

- 前端在 `web/` 执行 `npm run dev`。局域网地址 `172.20.4.2` 是已记录开发地址，网络变化后重新核对并更新精确 origin，不用通配白名单。
- 就绪检查：`http://127.0.0.1:8080/health/ready`。Go 源码修改后须编译并重启才生效，前端由 Vite 热更新；内存会话在后端重启后失效，沿用原凭据重新登录。
- 后端验证：根目录 `go test -race ./...`、`go vet ./...`。前端命令在 `web/` 执行：`npm ci --ignore-scripts`、`npm run typecheck`、`npm test`、`npm run format:check`。
- 浏览器回归使用既有 Chromium 与模拟 API，不安装系统浏览器，不连接真实邮箱；可用 `NEKOPORT_CHROMIUM` 指定浏览器，用 `NEKOPORT_UI_SOURCE=1` 在临时测试服务器验证源码，或 `NEKOPORT_UI_URL=http://127.0.0.1:5173` 复用现有开发页。局域网兼容性也需验证。
- 发布构建先前端后 Go，关闭 CGO 并用 `-buildvcs=false`；本地镜像使用 `docker build -f deploy/Dockerfile -t nekoport:local .`，可用 `docker run --rm --network none --read-only nekoport:local version` 验证入口；Docker 保持非 root、tini/exec、bridge 网络。不声称尚未执行的镜像构建或发布已经完成。
- 详细启动、测试、部署与恢复流程见 [开发文档](docs/development.md) 和 [README](README.md)。

# 测试账号

- 全新数据目录首次 serve 自动创建 `admin` 与 32 字符随机密码，仅在该次启动控制台输出；应用不生成明文账号密码文件，只保存用户名和密码哈希。已有账号不重新输出或重置，手动 init 仍可选；主密钥为数据目录中的 master.key。
- 除用户授权的首次生成密码控制台输出外，不在文档、其他日志或回复中记录明文秘密，不擅自重置已有账号/密钥，不生成或更新 dev-account.json，不依赖旧明文凭据文件恢复/重置账号。不从相邻项目读取或复制私有账号、数据和凭据。
- 自动验证使用临时数据、本地 TLS IMAP/SMTP 模拟及假接口，不为测试修改真实密码、发送邮件或触发真实全历史导入。
- 生产仅挂载 `./data:/var/lib/nekoport`，主密钥默认在数据目录的 `master.key`；init/serve 在缺少密钥且无已有加密凭据/引用时原子生成 0600 密钥，已有、空或无效密钥均不覆盖。旧双挂载部署须原样迁移旧密钥；全新数据目录首次 serve 自动初始化访问账号，也可显式 init 自设账号，不预置邮箱。

# 常见问题

- **邮件安全**：后端净化、DOMPurify、sandbox iframe 和独立 CSP 必须保留。远程图片默认拦截，CID 仅允许受控非 SVG 图片；HTML 可同源供宿主测量，但绝不能同时允许脚本。
- **邮件显示**：HTML 保留安全样式和原始配色，平台不限制最大宽度，遵循邮件自身 width/max-width，仅将根与外层布局容器水平居中并自动撑高，保留文字对齐及内部间距；纯文本用 React 文本节点的 pre，铺满阅读区、不设最大宽度，保留缩进和长行折行。不要恢复平台配色覆盖或内部纵向滚动条。保留常用 Flex/Grid、中文字体及条件样式；前后端样式规则同步维护，规则放宽时升级 HTMLVersion 并从本地原始 MIME 恢复旧缓存。
- **地址显示**：API 派生可读名称/地址，解码 MIME 字符集并去掉语法引号；保留原始 From/To/CC/Reply-To 用于回复发送，不全局删除名称内部引号，不要求旧记录重新同步。
- **访问主机**：不执行 HTTP Host 白名单校验，允许实际域名/局域网地址；写操作仍检查 Origin（同请求 Host 或精确配置的额外来源）、会话和 CSRF。反代保留原 Host；不通过信任任意转发头或通配 Origin 扩大权限。
- **请求恢复**：AbortError 不可转换为成功数据或普通网络错误；读取成功只清除对应刷新提示，不清除无关发送/保存错误。后台读取须串行合并并防止旧结果覆盖，写请求不自动重试。批量操作仅在请求成功后清理对应选择，保留请求期间新增选择；列表成功刷新后剔除不在当前列表的选中 ID，失败不清空选择。
- **同步一致性**：远端身份包含账号/目录/UIDVALIDITY/UID；游标与邮件批次原子提交，嵌套进度深拷贝，失败不得推进游标。近期筛选使用内部接收日期；失败/取消/部分扫描禁止删除扫尾，缺失 UID 二次确认，NoSelect 不冒充目录消失。增量由各可选择目录的 IMAP IDLE 或手动 quick 触发，不再快速轮询，旧 quick 周期字段不参与调度；禁用不支持 IDLE 时的 NOOP 回退。EXPUNGE/FETCH/标识变化、重连补偿及本地移动触发 history/reconcile 核对，移动须覆盖来源和目标，不能按序号删除或跨目录按 Message-ID 去重。保留任务期间的事件；认证拒绝/不支持 IDLE 等待配置更新，其余断线退避重连，25 分钟续期。目录发现及定时完整对齐以账号设置为准，连接数受邮件服务配额限制。同步日志按 quick 与完整对齐（含 history）分别保留每邮箱最近 5 次终态任务；进行中/暂停任务不淘汰。计数与索引同事务提交，每任务保留最近 200 条过程明细、完整累计统计，旧任务不补造明细。详见[IDLE 生命周期](docs/development.md#imap-idle-生命周期)。
- **构建缺包**：旧部署密钥目录只用根级 `/secrets/` 忽略，当前密钥随 data/.data 一并忽略；`internal/secrets/` 是必须提交的 Go 源码；本地构建通过后还需用 Git 跟踪与可提交文件构成的干净目录验证，避免被本机未跟踪源码掩盖缺包，不用 go get 安装本模块自身。
- **故障保护**：持久化写失败冻结变更，恢复不跳过中段损坏或缺失提交；不确定 SMTP/归档任务禁止自动重放。认证错误保留脱敏阶段信息，超时/断开/SMTP 4xx 不冒充确定的密码错误。
- **局域网写信**：HTTP 来源不能依赖 crypto.randomUUID；用 getRandomValues 生成发送幂等键，组件只初始化一次，重试复用，不改用 Math.random。
- **草稿请求**：保存只提交可编辑字段、正文和 revision，不回传 updatedAt/bodyId/attachments；后端兼容忽略旧页面附带的只读字段，不能把空时间解码到 time.Time。更新时间、正文对象引用和已有附件均以服务端为准，保留修订冲突检查。
- 当前未修复问题集中在 [开发文档的待处理问题](docs/development.md#待处理问题)，运行配置以当前持久化状态为准，不照搬历史快照。

# 其他备忘

- 回复使用中文，每次先称呼本会话已取得的 Git 用户名；未经明确要求不执行 Git 操作，不使用 sudo/root 或借助 Docker 绕过权限。
- “提交变更”表示 commit 并 push，除非另行说明；每次 commit 前更新 `docs/changelogs/YYYY-MM.md`，每条记录带时间戳。
- AGENTS.md 只记录长期约束、关键操作信息及文档入口；变更历史、验证结果与运行快照放入[月度 changelog](docs/changelogs/README.md)，详细流程和待办维护到开发文档，不再逐条追加到本文件。历史时间未知时标注原日期与迁移时间，不伪造时分。
- `../v2way` 只读作为视觉与构建参考，不修改相邻项目。平台采用深灰 `#181818`、面板 `#202020`、蓝色 `#007acc`、边框 `#323232` 与 4px 控件圆角。
- 页面字体为 Consolas / `LXGW WenKai` / monospace；中文文件实际为 LXGW ZhenKai（霞鹜臻楷），别名沿用 v2way。资源及授权见 `web/src/assets/fonts/`、`web/public/licenses/`，写信正文继承页面字体。
- 登录后外框参照 v2way：20px 留白、1px #353535 边框、10px 圆角与阴影，宽度不超过 720px 时全屏无外框；抽屉导航和遮罩定位在外框内。保持登录页等分布局及移动端适配；登录后默认邮件概览；侧栏邮箱账号默认全部折叠，最多展开一个，重复点击可收起；标题复用顶部直角菜单样式，展开时左侧蓝色高亮，不额外拉大账号间隔。下拉统一使用自定义 Select，浮层使用浏览器顶层显示并按视口空间定位，避免被弹窗裁切；修改密码使用 switch；同步设置在独立 Modal，对齐历史默认折叠。邮箱文件夹按默认角色优先，自定义文件夹在后按名称自然排序。阅读区归档仅移动当前邮件到同账号归档目录，无目标或已归档时禁用。
- 账户修改须验证当前密码、修订与 CSRF；新密码至少 8 字符、最多 72 UTF-8 字节，提交成功撤销会话/SSE。写信未保存关闭前阻止主动修改。
