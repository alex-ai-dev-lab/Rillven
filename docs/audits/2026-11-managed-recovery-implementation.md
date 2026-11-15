# 定价、渠道身份与托管恢复实施验收

日期：2026-11-15。工作树：`repair-20261017`，`main`，基于 `b7788ff39176337f02b5067ca96a85afa2715584`，修复已提交为 `75717ba`。验收在改名前完成，未部署或修改生产配置。当前 Rillven 配置命名与旧变量别名见 [ADR-024](../decisions/024-rillven-rename.md)。

本报告记录[源码方案](2026-11-transparent-failover-source-plan.md)的实际交付范围；调查文档中的生产现状和拟议能力不能替代本报告。设计约束见 [ADR-023](../decisions/023-managed-conversation-recovery.md)。

## 已实施行为

| 范围 | 实际行为与主要源码 |
| --- | --- |
| 定价保存 | `model-ratio-form.tsx` 统一提交当前编辑器和父表单的快照；`model-pricing-draft.ts` 保留显式零值、拒绝改名碰撞、避免错误 JSON 清空定价。保存失败保留草稿，双击只有一次提交，导出和模式切换先合入草稿；编辑器正文可滚动，底部保存可达。 |
| Responses 工具 | `responses_tool_shape.go` 区分 malformed 与目标协议不可表达。全局默认 Chat 无法表达 namespace/custom/deferred 时，在渠道确有能力且无显式端点约束的条件下回退原生 Responses。显式路由仍生效；不会通过删工具、改名或丢参数伪造兼容。 |
| 独立身份 | `channel_identity.go`、`upstream_client_profile.go` 持久化每渠道加密种子；凭据内容或稳定 OAuth 主体映射到固定 slot，重排 key 和正常刷新不换设备。session/thread 再按调用者和逻辑会话隔离。HTTP/WS、body/header 使用同一解析；管理员可预览、设置独立 ID、按版本重置。 |
| 出口策略 | `HTTPClientOptions` 区分 inherit/direct/proxy，HTTP、WS、SSRF 和已保存渠道维护请求沿用该策略。必需代理缺失或配置错误时失败，不静默直连。 |
| 托管会话 | `conversation_replay.go`、`conversation_response.go`、`model/conversation*.go` 保存 owner 隔离的加密不可变响应图，映射 `resp_renew_...`。鉴权后先恢复上下文，再选择目标并投影请求。原通道确认 `store:true` 时首试可用原生增量；其他尝试重建完整已知历史。 |
| 故障切换 | HTTP JSON/SSE/WS 共用恢复，保留函数/custom 调用、call ID、命名空间和已完成工具结果。已知 compaction capsule 展开其完整输入，未知父历史不被删成空对话。分支各自延续；A→B 后下一轮优先 B。 |
| 流与资金 | 复用 `stream_staging.go`。realtime 保留首语义前切换，输出后不拼另一条流；atomic_turn 缓冲整轮。终态文字/工具参数和工具身份与已发事件校验一致，存储准备和既有账本结算通过后发布终态。receipt 持久化结算意图，重启恢复不重新生成模型输出。 |
| OAuth | 按实际 refresh grant 共享数据库 claim/sent/received 状态；锁后重读渠道，CAS 更新凭据及配置版本。取消不丢已收到的 rotation receipt；已发送且结果未知时禁止重放旧 grant。刷新新增邮箱保持先前主体。 |
| 账户协调 | 渠道可开启同账户共享 FIFO/并发租约；账户、模型、显式代理分别冷却。排队结束重查冷却和配置版本；429/401/代理 407 归入对应范围，读取明确耗尽的通用 quota headers。迟到成功不能清除新冷却；本地容量、存储和计费错误不污染上游健康。 |

普通 Chat/Messages 自带完整历史的转发继续沿用现有流程；新持久 `previous_response_id` 恢复入口为 Responses HTTP/WS。没有把不同客户同名 session 或相同正文合并成一段对话。

## 关键逻辑与成本

- 主数据库为状态事实源；查找错误和未命中分开。逻辑 owner 包含用户、令牌 ID 和令牌摘要，权限与渠道可用性每请求重新检查。
- AES-256-GCM 使用独立 keyring；AAD 绑定 owner、响应、session、父链、渠道、slot、出口/配置版本、原生状态及可迁移标记。状态 SQL 日志禁用展开值，避免正文、密文和刷新结果进入诊断日志。
- 节点只保存本轮增量，每 32 层保存一次完整快照；分支不使用共享可变最后响应。原始请求不变，各次重试独立投影，旧成功不会覆盖新分支。
- 增量文本/参数使用 SHA-256 校验，避免另存一份完整流文本。工具身份单独有界存储；整轮缓冲受单请求和进程级上限约束，所有退出路径归还预留。
- shadow 在成功计费后尝试保存，观察失败不改变原始响应、ID 或正常可用性。只有来源渠道已经转为 managed，才采用此前 shadow 的原生 ID/capsule 记录。
- 成功上游输出一旦进入持久准备，存储/结算故障不会触发再次生成。账本仍是唯一资金权威；逻辑 ID 只有发布后的 checkpoint 才能继续使用。
- 跨渠道重建可能增加输入用量并丢失上游缓存；按现有计价契约结算真实用量。未进行新吞吐基准，不能将有界分配和减少文本副本描述为生产吞吐提升。

## 启用与回退

先在所有实例配置独立密钥，再迁移表和灰度开启。以下是格式示例，占位值不是有效密钥：

```text
RENEWAPI_STATE_KEYS={"k1":"<base64-encoded 32 random bytes>"}
RENEWAPI_STATE_ACTIVE_KEY=k1
```

各实例使用同一 keyring；数据库与密钥分开保护和备份。新增 active key 后，旧记录仍依赖旧 key。渠道身份长期保存，不能仅等待会话 TTL 就删除旧 key；当前没有批量重加密工具。原生 OAuth 刷新也要求此 keyring。

渠道编辑器的高级设置提供：

| 字段 | 含义与默认 |
| --- | --- |
| `client_identity_mode` | 默认 inherit；可 per_channel / shadow / legacy / disabled。旧配置继续原行为；全局身份设置也可选择独立作用域。 |
| `client_profile` / `client_version` | 默认沿用已有 UA 设置；可显式选择匹配协议的 Codex/Claude。默认版本仍为 Codex `0.59.0`、Claude `2.1.80`，没有推断或随机生成新版组合。 |
| `egress_policy` | inherit 默认沿用原代理逻辑；direct 不使用代理；proxy 必须配置渠道代理。 |
| `conversation_recovery` | off 默认关闭；shadow 观察并尝试保存自包含轮次；managed 保存并透明恢复。 |
| `stream_commit_mode` | realtime 默认；atomic_turn 完成整轮再交付，增加首输出延迟。 |
| `upstream_coordination` | 默认 false；true 启用同账户的共享队列、租约及额度冷却。 |

未保存渠道没有持久设备身份，须保存后预览/配置；未保存的模型列表检查只使用显式出口策略。已有 managed 响应 ID 在关闭新会话 admission 后仍可读取至到期。回退必须保留支持新表/新 ID 的版本，不能直接换回不认识 `resp_renew_...` 的旧镜像。

建议先少量渠道独立身份预览，再 shadow，再 managed；需要整轮完整结果的任务开启 atomic_turn。不要在长会话中频繁重置身份。升级、切换 keyring 或回退前排空旧请求/WS。

### 默认限制

`conversation_recovery_setting` 可通过现有 option API 配置，服务端具有硬上限：

- 空闲保留 24 小时，最长 7 天；在途请求具有 600 秒保护/总期限。
- 重放正文 16 MiB，每会话密文 64 MiB，每 owner 密文 512 MiB；最多 128 层、3 次生成尝试，仍取现有路由预算中更小的限制。
- 每轮缓冲 16 MiB，进程总缓冲 256 MiB；只使用有界内存，不超限后偷偷改为 realtime。
- 维护每 30 秒运行，每批最多恢复 64 个 receipt、清理 32 个会话。OAuth rotation receipt 保留 7 天；消耗过的 grant 墓碑继续保留。
- 账户并发默认 4，FIFO 等待队列 32，等待 3000 ms。环境变量分别为 `RENEWAPI_UPSTREAM_CONCURRENCY`、`RENEWAPI_UPSTREAM_QUEUE`、`RENEWAPI_UPSTREAM_WAIT_MS`，上限 64/256/30000。运行租约 45 秒，每 10 秒续租，丢失租约取消当前请求。冷却窗口最长 24 小时。
- pool scope 行与 OAuth grant 墓碑目前长期保存；过期队列项和会话有清理，不声称所有新表都有自动压缩。

本地托管与上游 `store` 独立：managed/shadow 可保存 `store:false` 的请求，管理界面已明确提示。要求本地不落盘的渠道使用 off；已交付的托管 ID 仍需按保留期排空。

## 验证结果

所有验证使用隔离数据库、合成请求及假上游，没有重放用户线上正文。

| 检查 | 结果 |
| --- | --- |
| Go | 全仓测试、vet、build 通过；核心恢复/资金/身份/刷新/队列及受影响 HTTP/WS adapter race 通过。 |
| 资金崩溃恢复 | 30 组独立进程异常退出：3 种账本模式 × 钱包/订阅 × prepare 前、prepare 后、settle 后、publish 后、delivery 后。重启后双次 reconciliation，余额和恢复正文一致，无重复扣费。 |
| 持久数据库 | SQLite、MySQL 5.7.44/8.4.0、PostgreSQL 9.6/16.4：2.24 MB 状态、receipt/资金意图、owner 隔离、容量回滚、身份/slot、OAuth 记录/CAS、FIFO/租约/冷却通过；四外部版本加入 CI。 |
| 协议与故障 | JSON/SSE/atomic A→B→B，WS 断连重连和 function/custom 工具结果；原生增量首试/重试完整快照；已知 capsule；未知历史拒绝；shadow 后端故障被动；AAD 绑定篡改、终态正文/工具身份冲突、容量/取消/partial-write/结算错误通过。 |
| 前端 | 234 项 Bun 测试（500 断言）、TypeScript、改动文件 lint、生产构建通过。未修改文件的历史全量 lint 问题不归为本次通过。 |
| Playwright | 真实后端保存/刷新显式 0；改名和导出；HTTP 500 保留草稿后重试；双击单写；重名拒绝；JSON/可视模式草稿一致；独立渠道 ID 真保存；恢复/出口/队列开关读回通过。 |
| 视口 | 1600×900、1366×768、1093×614、390×844 保存按钮滚动后可见可点击。1093×614 是缩小 CSS 视口，未声称真实浏览器 125% zoom；未声称所有尺寸首屏都显示底部按钮。 |

可复用测试入口：`scripts/qa/model-pricing-recovery.cjs`、`service/conversation_restart_test.go`、`controller/conversation_integration_test.go`、`model/conversation_test.go`。本地日志位于 `D:/Code/renewapi/audit/20261115-final-*.log`，数据库日志在 `20261115-managed-db/<variant>/final-test.log`，浏览器报告在 `20261115-pricing-browser-final/report.json`。首次 PostgreSQL 运行遇到 Windows 管理员直接启动及进程树等待问题，修正测试启动器后通过；首次扩展浏览器等待了旧渠道保存 URL，改为实际 config URL 后通过。未放宽产品断言。

## 明确的能力边界

- 已知 compaction 展开为完整原文；没有自动目标端重压缩/上下文窗口规划。超出恢复上限或目标窗口时明确失败，不截断历史。客户端仍可使用已有显式 compact API。
- 未实现私有 Files 上传、container/未知 `item_reference` 迁移。外部 URL、私有资源、未知服务端工具历史保守绑定原渠道、凭据和配置；无法证明完整归属时不迁移。
- 隐藏推理、外国签名和旧版未知密文没有可移植解密契约。保留可见消息与工具结果，不能声称恢复了私有推理状态。
- realtime 已暴露的未完成输出不能撤回或透明替换，也不保存为已完成 checkpoint。atomic_turn 仅在可重试/无未知副作用条件下提供整轮切换。
- MCP 等可能产生副作用的服务端工具，结果未知时不自动跨提供方重放。客户端没有幂等键的新 HTTP 重发不能保证 exactly-once。
- 配额只接入明确的通用 `x-ratelimit-remaining/reset-{requests,tokens}` 和已有 Retry-After。未将 WHAM 的厂商专有额度窗口完整接入调度；同真实模型的不同请求别名仍可能分桶。
- 没有新增 uTLS/TLS 指纹试点、伪造遥测或绕过上游授权。渠道身份隔离只能减少网关制造的身份冲突，不能保证上游不风控、不封禁；第三方中转后台身份不可验证。

上述工具/资源/厂商能力按方案的 P5 分项边界保留，不能把本轮结果称为任意客户端、任意资源、任意中途失败都无感切换。
