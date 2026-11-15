# Rillven Actions 发布与无缝部署

Status: complete
Created: 2026-11-15
Completed: 2026-11-15

用户授权 Actions 构建镜像到 Releases，服务器代理下载、无缝切换，确认正常后删除
本次中间文件和备份；只在 main 提交。补充确认仓库名为 **Rillven**（R 大写）。

## 发布身份

- GitHub：[alex-ai-dev-lab/Rillven](https://github.com/alex-ai-dev-lab/Rillven)。
  Repository ID `1234291482`、默认分支 main 保持，旧地址可用。
- 部署源码：`cb40c8ff5bda8efcffffe62d053901d2ed51b28f`，包含 `75717ba` 的定价/
  工具/独立身份/托管恢复和 `1d9c3e5` 的 Rillven 改名。
- [完整 Actions 43690829405](https://github.com/alex-ai-dev-lab/Rillven/actions/runs/43690829405)
  的后端/前端、MySQL 5.7/8.4、PostgreSQL 9.6/16、两组真实浏览器和双架构构建全部通过。
- [源码预发行 Release](https://github.com/alex-ai-dev-lab/Rillven/releases/tag/rillven-build-cb40c8ff5bda-43690829405)：
  `rillven-build-cb40c8ff5bda-43690829405`，包含 amd64/arm64 镜像、配置及 QA 包。
  `VERSION` 仍为 v1.5.1，没有创建新正式版本、重指向标签或覆盖附件。
- 生产镜像 `rillven:sha-cb40c8ff5bda`；amd64 归档 SHA256
  `a4e06f8b416d50ba77df170fd0180243759f330ae4de67272d5485d9cb35ba14`。
  Docker config ID 为
  `sha256:99d0d8ba53a378c559b3472d84cb7c95775b91a529e93659770cf04a30b62d04`。
  Release inspect 使用 OCI manifest 身份；已通过归档 config 字节、RootFS、架构和
  revision/title 标签验证二者对应，未将格式差异误当作镜像不一致。

首轮 Actions `43689484454` 在 JSON 边界门禁失败：三个恢复文件仅为 RawMessage
直接导入标准库。改用既有 `common.RawMessage` 别名，`go test ./service ./controller`、
相关 vet、原始基线 JSON 检查及 tidy 通过后提交 `cb40c8f`；保留完整发布门禁。
本归档及仓库大小写文档更新不改变已部署程序，另以 main 文档提交记录。

## 部署过程与数据

部署根目录 `/home/ken/services/new-api`，站点 `https://router.108848.xyz:1443`。
旧容器 `new-api-v1-5-1` / project `renewapi-v1-5-1` / 3027 保持运行，新容器与 project
均为 `rillven-cb40c8ff5bda`，独立端口 3028。Compose dry-run 确认只创建新容器。

服务器通过 `http://127.0.0.1:10808` 代理下载准确 Release；低速传输改为明确 Range
分段续传，逐段核对 Content-Range，完整归档通过 GitHub asset digest 和 CHECKSUMS。
没有使用 `/releases/latest`、GHCR 或本机构建替代 Actions 产物。

SQLite 实际文件为 `data/one-api.db`，日志仍在同一数据库。使用在线 backup API
制作初始和迁移前一致备份，在网络隔离容器中执行副本 `migrate --up/--check`，
验证 47 张原有表结构和全部业务行指纹不变，仅新增以下 10 张空表：

`channel_credential_slots`、`channel_identities`、`codex_refresh_journals`、
`conversation_checkpoints`、`conversation_owner_usages`、`conversation_resources`、
`conversation_sessions`、`conversation_turn_commits`、`upstream_admissions`、`upstream_pool_states`。

随后对生产执行相同加表迁移。最终 quick_check=ok，112 个渠道、22 个用户、65 个令牌、
86 项配置；关键定价/端点/全局身份选项及全部渠道 setting/param_override/header_override
与迁移前副本一致。会话密钥、挂载、原环境及 BILLING_LEDGER_MODE=off 保留。
站点自定义名称 Interface Zero 保留；新身份/托管恢复开关没有批量开启或重置。

新增独立状态 keyring `/home/ken/services/new-api/state-keys.env`，权限 0600、属主 ken，
active ID `k20261115`，由运行 Compose env_file 引用。密钥不写入源码、证据或输出；
它是永久运行配置，后续不得随下载包清理或随意重新生成。

## 切换与验收

2026-11-15 08:09 UTC 热切换（本地 UTC+8 为 16:09）。适配/校验 Caddyfile，并比较
实际运行 JSON，只改变两处 3027 → 3028 上游；候选文件自动 hide 路径按实际加载路径
规范化后比较。保留 `read_body_idle -1s`、`flush_interval -1`、`stream_close_delay 5m`，
使用 reload，未重启 Caddy。

- 候选和公网各 7 项 API：Responses 文本 SSE、namespace custom 流/非流、Chat
  function 流/非流、Messages tool 流/非流。验证工具名/输入、协议终态、成功日志和计费。
- 候选 Responses 长流 80.24 秒，公网 87.43 秒；均在 60 秒后继续输出并收到
  `response.completed`。没有用单纯 HTTP 200 或心跳作为长流通过依据。
- 旧实例请求在切换前已输出，跨 reload 后继续并在 81.12 秒收到终态、成功计费。
- 切换专项 15 次、准备至旧实例退役后全程 1322 次入口状态监测，均无失败。
- 切换前打开的旧页面能继续加载模型管理懒加载分包，原管理员会话延续；新页面保持
  Interface Zero 标题，显示 Rillven 页脚及“保存模型价格”按钮，初次加载无失败请求/
  pageerror。生产不修改价格做测试；真实保存、零价格、失败重试及多视口由源代码
  验收与 Actions 浏览器检查覆盖。
- 旧静态缓存核对 1631 个资源、65,808,628 字节均已完整存在。保留既有 Caddy 缓存，
  验证 7 个旧入口资源的字节摘要。清理后工作站再次检查公网状态和 7 个新入口资源。

真实 API 使用既有管理员部署测试 token id 1、绑定渠道 267、模型 gpt-5.6-sol，均为
合成测试正文。token 仅在服务器内存读取；没有执行模型返回的工具。

初轮 namespace 工具调用已成功，但验收脚本误要求可选 namespace 回显；直连上游
证明它原生只返回 `name=emit/input=hello`，据此校正契约后复验，原失败记录保留。
另有候选和公网各一次 `do_request_failed`（约 60/45 秒，HTTP 503、零扣费），
只重试未通过项后成功；这两次不能算作 API 零错误。浏览器辅助检查曾因后台标签、
行选择器/编辑器字段假设超时；生产 UI 验收范围以上方实际完成项为准。

旧容器入站连接连续归零后执行优雅停止，330.38 秒退出、退出码 0、非 OOM。
运行 `.env`/root docker-compose.yml 同步到新 image/project，永久配置保持可读和私密。
新容器 healthy、RestartCount=0，未发现 runtime panic/fatal。

## 清理与证据

完成上述验收、导出安全证据后，删除本次 13 个下载包/分片/响应头文件、两份数据库
及配置备份所在目录、隔离数据库副本/旧二进制/临时脚本/监控所在目录、部署锁和
已停止旧容器，共约 1.06 GB。清理后再次通过数据库、容器、公网及静态资源检查。

保留数据、业务日志、当前镜像、永久 keyring、运行 Compose、Release 身份元数据及
旧浏览器静态缓存。不清理无关历史备份、旧 Releases/tags/Actions 或其他服务。

运行 Compose：
`/home/ken/services/new-api/releases/rillven-build-cb40c8ff5bda-43690829405/candidate.compose.json`。
安全验收证据：`D:/Code/renewapi/audit/20261115-deploy-rillven/evidence/`，另有 Actions/
Release 元数据、清理日志、工作站外网复验。目录中不含生产凭据或数据库副本。
