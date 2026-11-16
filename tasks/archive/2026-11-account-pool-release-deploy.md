# 共享账号池源码发布和服务器切换

Status: complete
Created: 2026-11-16
Completed: 2026-11-16

用户授权提交 main、GitHub Actions 构建离线镜像到 Releases、服务器无缝切换，
并要求任务完成后关闭本地电脑，服务器继续运行。本次不自动开启现有渠道的账号池模式。
完整真实账号池验收继续留在 `../active/2026-11-shared-account-pool.md`。

## 源码与发布

- 功能提交 `62dada0246c719c1453c16f588ec00c984c67f08`，依赖校验和修复
  `2817712a52ed13a09fd34622d51ee1bc8bc9ebc7`，均已推送 main。
- [Actions 43892087235](https://github.com/alex-ai-dev-lab/Rillven/actions/runs/43892087235)
  的后端/前端、四版本数据库、两组实际浏览器、双架构镜像及附件回下载检查全部成功。
- [源码 Release](https://github.com/alex-ai-dev-lab/Rillven/releases/tag/rillven-build-2817712a52ed-43892087235)
  已公开，13 个附件，非草稿、源码预发行；标签指向上述修复提交。产品版本仍为 v1.6.0，
  不创建新正式版本、不改变正式 Latest、不重指向标签或覆盖附件。
- 镜像 `rillven:sha-2817712a52ed`；amd64 归档 44,646,892 字节，SHA256
  `1b089712fa881c8283690984af4ac06544a9e283c82f6387175c2aab6398386a`。
  镜像 config ID `sha256:28572a239d834d72a828504c8f45f91fa2aa1ecb430f466fcd45e2ca239e3cb3`。
  归档、Release digest、CHECKSUMS、配置字节、RootFS、linux/amd64、版本和源码标签已核对。

首轮 Actions `43891670617` 发现 BLAKE3 间接测试依赖缺少四条 go.sum 校验和，
go mod tidy 补齐后 tidy -diff/verify 通过。PostgreSQL 9.6 首轮在下载依赖时遇到
proxy.golang.org HTTP/2 INTERNAL_ERROR，未进入测试；保留完整门禁后重跑成功。
首轮已无法发布，取消剩余运行以释放构建队列；未删除历史记录。

## 部署与实际验证

服务器通过 3067 HTTP 代理下载。初始大分段超时会重新传输，改为保留精确 Range 前缀，
以小分段续传并核对 Content-Range，完整 SHA256 成功后导入。归档还携带同 config 的
edge 别名，docker load 更新了该别名；部署明确使用源码标签，原 v1.6.0 镜像保持。

在线 SQLite 备份先做隔离迁移：55 张原有表全部原始字段/业务行指纹一致，新增 11 张空表。
隔离容器禁用其他渠道并使用 slave 模式，避免副本刷新真实 OAuth grant；隔离副本上的
Responses custom SSE 和 Chat function 非流请求，工具语义、终态及完整结算通过后停止。

生产另做迁移前在线备份，执行 migrate --up/--check，检查保留渠道配置、选项、身份和
历史数据，quick_check=ok。候选 Compose/project/container `rillven-2817712a52ed`，
独立网络和本机端口 3030；dry-run 确认不重建旧实例。原环境、挂载、SESSION_SECRET、
off 计费模式、永久状态 keyring 及渠道设置一致。生产候选 custom SSE 终态和结算通过。

2026-11-16 15:27:36.481 UTC（UTC+8 23:27:36）Caddy reload，仅将两处 Rillven
上游由 3029 改为 3030，并增加上一镜像的静态资源缓存，保留其他站点及反代设置。
旧实例已开始输出的流持续 83.55 秒，跨 reload 后收到 response.completed，
actual_quota=applied_quota=8140，成功日志和账本均确认；切换后公网 function 调用也成功结算。

原版静态资源保存 1,634 个文件 / 66,153,306 字节；新入口 7 个资源及旧分包抽样 6 个
通过公网字节校验。生产最终 112 渠道、22 用户、65 令牌、86 选项，配置/keyring 一致，
新增 account/vault/历史表仍为空，未自动启用账号池功能。migrate --check 和 quick_check 通过，
3030 healthy、重启 0、Caddy 新上游两处/旧上游零处、3029 关闭；数据库仅由新进程
PID 1457218 和一个新容器持有写入挂载。原根 Compose/.env 已同步新 image/project。

## 异常与证据边界

隔离 custom 请求的首轮验收脚本误用 X-Request-Id，实际服务返回 X-Oneapi-Request-Id。
原请求工具/终态正确，保留失败记录后按唯一时间/token/channel 日志关联原账本确认结算，
修正后续脚本，没有为此重复请求。候选继承了旧 external 网络声明，dry-run 发现目标网络
不存在；改为由新项目创建独立网络后继续，生产迁移尚未开始时即修复该部署配置问题。

旧实例接收 SIGTERM 后停止监听、多个 worker 已退出，但未在 Docker stop 的 180 秒内
结束，最终于 15:33:48.945 UTC 被终止，退出码 137、非 OOM；不能写成优雅退出。
验证的旧请求已完成并结算，切换前两条新账本均 settled；旧程序退出停滞的根因未查明。

全程探测：旧端口 747 次 / 0 失败，候选 266 次 / 0 失败，公网 899 次 / 19 失败。
其中 18 次发生于候选部署之前，包含明确 DNS 失败（curl exit 6）和无 HTTP 状态的超时。
切换附近另有一次 5,012ms 超时（exit 28 / HTTP 000）；以完成时间减耗时计算，其请求
约在 reload 前 4 秒开始。Caddy 日志显示正常 reload，但不能据此确定超时原因或用户影响，
不能断言整个切换零失败。保留原始监测，追加正常 DNS、公网 hostname 固定到本机 Caddy、
候选直连共 60 次包含 DNS/连接/首字耗时的复验，全部通过。工作站 verified TLS 外网健康通过；
Windows curl 曾因系统证书吊销检查离线失败，使用 Python requests 正常验证证书后成功，
没有修改全局证书策略或跳过 TLS 验证。

真实账号池 A/B、U/V、quota/OAuth 故障、两个独立进程的共享账号流量及官方 TLS/H2 对照
仍待功能验收；这次镜像更新和旧模式部署验证不替代这些结果。此前 SQLITE_BUSY 的
具体 lock holder 仍未知，短重试源码已上线，但没有据此宣称生产根因消失。

## 收尾

证据：`D:/Code/renewapi/audit/20261116-account-pool-release-deploy/`，已导出安全 JSON
及原始非敏感状态日志，不含凭据或数据库副本。服务器本次在线备份、隔离副本、旧镜像、
停止的旧容器及静态缓存保留，未清理历史 Releases/tags/Actions 或其他服务。
回退前需核实新增状态与 OAuth/资金兼容性，不以恢复旧快照倒退新版写入。

运行 Compose：`/home/ken/services/new-api/releases/rillven-build-2817712a52ed-43892087235/candidate.compose.json`。
归档另以 `[skip ci]` 提交 main，不再构建业务镜像；提交/推送及最终健康确认后执行
用户授权的本地延时关机，仅关闭本地 Windows 电脑。
