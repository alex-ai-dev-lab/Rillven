# Rillven v1.6.0 正式发布

Status: complete
Created: 2026-11-15
Completed: 2026-11-15

用户明确要求正式版，不要预发布版；沿用本会话 Actions → Releases → 服务器代理下载、
无缝切换和本次备份/中间文件清理的授权，仅在 main 维护。

基线：main `e1a8282`，线上已验证源码 `cb40c8f`，容器 `rillven-cb40c8ff5bda` / 3028。
现有产品版本 v1.5.1。本次包含新增兼容能力，发布 v1.6.0 / `rillven-v1.6.0`，
镜像 `rillven:1.6.0`，必须 prerelease=false、draft=false、Latest 指向正式标签。

更新 VERSION/CHANGELOG 及仓库 URL 大写 R，保持业务行为和历史标签/附件不变。
主分支发布准备提交跳过自动源码包，随后对正式 tag 手动触发原有完整 Actions 门禁；
手动正式标签执行不跳过质量、数据库、浏览器或镜像检查。

服务器沿用永久状态 keyring 和原配置，新版使用独立 project/容器/端口预热，
验证后 Caddy reload，排空旧请求，再清理本次中间文件和备份。
证据：`D:/Code/renewapi/audit/20261115-release-v1.6.0/`，不包含凭据或数据库副本。

## 发布身份与门禁

- 正式 [Rillven v1.6.0](https://github.com/alex-ai-dev-lab/Rillven/releases/tag/rillven-v1.6.0)，
  Release ID `445928949`，`draft=false`、`prerelease=false`，Latest 指向正式标签。
- 产品源码 `565ff72b4a12d070a1440e24f745a2dee34156a4`，镜像 `rillven:1.6.0`。
- [Actions 43696597587](https://github.com/alex-ai-dev-lab/Rillven/actions/runs/43696597587)
  的后端/前端、MySQL 5.7/8.4、PostgreSQL 9.6/16、两组浏览器、双架构镜像和
  附件回下载校验全部成功；13 个正式附件完整。
- amd64 归档 `rillven-1.6.0-linux-amd64.tar.gz`，44,490,402 字节，SHA256
  `f8344abbb242af1dff9d381aff42008a06af6e57cc8579805ce94500913e747a`。
  本机 Docker image ID `sha256:2c95802f34dfb6f0af48f2069d430816db5c3597d937317449556b4ee89d32ee`。
- 本地发布元数据测试、版本身份检查、common 测试、改动前端 lint/格式及 diff 检查通过。
  产品 tag 不移动，正式附件不覆盖；归档提交使用 `[skip ci]`，不生成新的源码预发行包。

## 上线与验证

服务器通过 `http://127.0.0.1:10808` 代理，以 6 个明确 byte ranges 下载正式附件；
核对每段 Content-Range、完整 GitHub asset digest/CHECKSUMS、镜像 config/RootFS/
架构/源码标签后导入。独立容器与 Compose project `rillven-v1-6-0`，端口 3029。

SQLite 在线一致备份和隔离副本 `migrate --check` 通过，副本全部表结构/行指纹不变，
生产 `--check` 无待执行迁移。本次不修改业务代码或迁移；保留原环境、挂载、
SESSION_SECRET、off 计费模式、永久状态 keyring、Interface Zero 名称和渠道设置。

2026-11-15 09:17:20 UTC（UTC+8 17:17:20）完成 Caddy reload，仅将两处上游
3028 改为 3029，并添加上一源码版静态资源缓存。保留长流反代配置，不重启 Caddy。

- 候选与公网各验证 Responses namespace custom SSE、Chat function 非流；工具名、
  流增量与终态参数、协议终态、成功日志和实际计费通过。
- 候选长流 79.67 秒，公网长流 80.29 秒；60 秒后继续输出并收到 `response.completed`。
- 旧实例切换前已输出的长流持续 80.45 秒，跨 reload 正常结束并计费。
- 切换专项 15 次探测无失败。全程监测的最终数值与异常见下方收尾记录，不能写成零错误。
- 旧浏览器会话延续，模型管理懒加载成功；新页面版本 v1.6.0、自定义标题和大写 R
  仓库链接正确。模型定价页与编辑面板的“保存模型价格”均可见、可用；不修改生产价格。
  真保存及失败重试由此前源码验收和本次正式 Actions 覆盖。
- 工作站外网 7 个新入口资源通过；旧入口资源 7 个摘要一致，运行 Compose 可由 ken 读取。
- 旧实例入站连续归零后 3.92 秒正常退出（退出码 0、非 OOM），新实例 healthy、重启 0。

## 异常与边界

验收初轮把自由文本工具末尾换行当成失败。观察到上游 `hello\n` 后，明确允许
`hello` 或 `hello\n`，同时新增 delta/done/item/completed 参数逐字一致检查；没有
改变生产转换逻辑。可选 namespace 回显允许缺省。两次因末尾换行失败的请求实际成功计费，
原始失败证据保留，不将所有尝试算成一次成功。

候选首个请求同时遇到 SQLite `SQLITE_BUSY`：结算和补偿意图写入均失败，日志
`billing_error` 的公共提示被映射为模型繁忙。该合成测试账本仍为 reserved，预扣、
实际和已扣额度均为 0，无余额损失；保留既有 21600 秒孤儿账本回收，不手工篡改账本。
此 SQLite 并发写入及错误提示边界未在本次版本发布中修复，后续请求均正常结算。

公网初轮两项请求均因上游 TLS handshake timeout 返回 503，零扣费；逐项重试成功。
这两次和上述一次计费错误均保留，不能声称 API 零失败。

持续监测在 09:26:00.652 UTC 有一次失败（耗时 3988ms），前后样本成功。旧实例已于
09:24:15.965 UTC 正常退出，新实例无重启，当时无相关 Docker/Caddy 事件；初始探测器
未保留 curl 退出码/HTTP 状态，原因未确定，不能将其归因于切换或断言用户实际中断时长。
保持完整原始监测记录，另以最近连续 90 次健康样本和 60 次包含退出码/HTTP 状态的
容器直连/Caddy 独立探测作为清理前稳定性门禁，不清零历史失败计数。

浏览器辅助检查曾因设置页自动重定向、折叠菜单不可见及面板不是 role=dialog 而超时；
按实际路由、编辑控件和标题完成复验，不将辅助脚本的选择器假设当成产品故障。

## 收尾

全程 1158 次入口监测有上述 1 次失败，切换专项 15 次及追加 60 次稳定性探测无失败。
最终 quick_check=ok，112 个渠道、22 个用户、65 个令牌和 86 项配置；关键选项、
渠道 setting、永久 keyring 和运行挂载一致，新容器 healthy、RestartCount=0。

导出 40 份安全 JSON/JSONL 证据后，删除本次备份目录、隔离数据库/临时脚本/监控目录、
13 个归档/分片/响应头文件、部署锁及已停止的旧容器，共 788,533,493 字节（约 789 MB）。
没有清理无关历史备份、GitHub Releases/tags/Actions、旧镜像或其他服务。

保留数据和业务日志、当前镜像、旧浏览器静态缓存、永久 keyring 及 Release 身份元数据。
运行配置：`/home/ken/services/new-api/releases/rillven-v1.6.0/candidate.compose.json`，
root `.env`/`docker-compose.yml` 已同步到正式 image/project；ken 可读取运行配置，
keyring 属主 ken、权限 0600。清理后数据库、健康、公网版本及 7 个新入口资源复验通过。

本地证据目录 `D:/Code/renewapi/audit/20261115-release-v1.6.0/evidence/`，另保留
浏览器结果、工作站外网验证、Release/Actions 元数据、清理及异常日志。文档归档单独
提交 main，不改变正式 tag 的产品源码；本地部署临时脚本用完后仅按本次目录清理。
