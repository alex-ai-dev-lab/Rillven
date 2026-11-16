# 共享账号池源码验收记录

日期：2026-11-16。基线 main `98a499224a081ffb3d127c2ca7cb8e90ea5a71f4`，
业务 v1.6.0 `565ff72b4a12d070a1440e24f745a2dee34156a4`。本记录覆盖本地源码验收；
追加授权的提交、Actions 发布和服务器切换见 `tasks/active/2026-11-account-pool-release-deploy.md`。

## 本轮修复与证据范围

| 场景 | 当前源码/隔离验证 | 边界 |
| --- | --- | --- |
| A/B × U/V 实际 HTTP/原生 WS | authenticated gateway + 本地真实 HTTP/WS 捕获，A→B、多轮 function/custom、新连接接手、owner 猜 ID 拒绝 | 上游账号是夹具，新 gateway 与共享 DB 在同一个测试进程 |
| 账号身份/轮转 | 相同主体/不同渠道同安装；不同 owner 独立；vault 轮转及旧凭据重新导入不倒退 | opaque 主体归一需明确配置，不宣称发现真实主体 |
| 官方生成逻辑 | 固定 Codex 源码；数字池/边界/版本向量、持久 UUIDv7 与 context allocation | 未运行官方完整客户端测试套件；未证明 TLS/H2 指纹一致 |
| 历史/引用 | 完整前缀、修改前缀拒绝、数字 9007199254740993、不透明资源及 affinity owner/account 检查 | 未上传/迁移私有资源，不删除未知密文获得成功 |
| 幂等 | 相同 key 重取、内容冲突拒绝、并发一个 claim；结果加密保存 | replay 是终态完整结果，pending/删除/过期不重新生成 |
| 额度/调度 | 迟到成功不能恢复耗尽窗口、reset 半开、共享 probe、禁用槽位和 quota 选号 | WHAM 的真实供应商/模型窗口仍需 live 验收 |
| refresh | 共享 grant journal、取消后保存、unknown 不重发、CAS 和禁用 multi-key 槽保护 | 无真实 OAuth rotation 故障注入 |
| 计费 | SQLITE_BUSY 注入短重试、回滚、重试不重复扣费；失败 recovery intent 返回值保留 | 2026-11-15 生产锁 holder 未查明，不宣称所有持久故障都能恢复 |
| 删除/恢复 | active 时立即阻止访问；独立删除记录导入阻止旧快照访问；重加密后可读 | 离线备份保留期、生产恢复流程尚未演练 |
| 画像升级 | 显式更新 profile 后 installation/session/thread 不变，过期画像不能覆盖 | 真实供应商对新版画像的接受程度需单独验证 |

测试发现并已针对源码修复：managed timeout 恢复原 Request、初次身份检查的 nil meta/无 DB、
正常渠道配置助手的 nil Request、PostgreSQL 浮点条件参数推断，以及 MySQL repeatable-read
竞争后的快照读取。未降低原有断言。一次全仓命令在前端重新生成 dist 时读取 embed 文件失败，
后续检查按前端 build 完成后再验证根包的依赖顺序执行。

## 最终检查

最终日志保存在 `D:/Code/outputs/rillven-account-pool-*`；外部数据库为该任务独立数据目录
`D:/Code/outputs/rillven-account-pool-db`，端口 39457/39484/39496/39416，测试结束后关闭。
没有使用或改动生产数据库、服务和配置。

| 最终检查 | 结果 |
| --- | --- |
| `go test ./... -count=1 -timeout=180s` | 通过，包含原有 HTTP/WS/进程崩溃恢复回归 |
| `go vet ./...`；`go build -o D:/Code/outputs/rillven-account-pool.exe .` | 通过 |
| `go test -race ./model ./service ./controller ./relay/channel -run 'TestAccount\|TestCodexRefresh' -count=1 -timeout=180s` | 通过；relay/channel 在此 pattern 下没有匹配测试 |
| 较早的完整相关 race pattern：account/managed/refresh/channel identity/ResponsesWS | 通过；最新数据库 current-read 修复后 account/refresh race 再次通过 |
| MySQL 5.7.44 / 8.4.0，PostgreSQL 9.6 / 16 | 最终 model account + durable context/identity/refresh/admission 集合全部通过，分别 8.390s / 12.780s / 11.809s / 11.313s |
| 前端改动文件 Prettier/ESLint、typecheck、lint:semantic、build | 通过 |
| `bun test` | 240 pass，0 fail，48 文件 |
| 前端版权检查；Go 改动文件 gofmt；`git diff --check` | 通过 |
| 改动 workflow 的 actionlint v1.7.7 | 通过；本机 shellcheck/pyflakes 不可用，未执行它们 |

GitHub Actions 尚未触发，源码中的 CI 数据库门禁已增加
`go test -run TestAccount ./model`。真实 A/B 和 U/V、两独立服务进程、官方传输捕获、生产迁移及
灰度未执行。完整方案的真实验收部分继续保留在 active task，不能以本记录的局部测试替代。
