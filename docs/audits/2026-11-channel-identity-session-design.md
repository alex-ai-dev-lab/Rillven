# 2026-11-15 每渠道独立身份与跨渠道会话方案

状态：设计及源码比较，尚未实施。用户明确要求每个渠道独立配置设备、安装 ID，并继续检查参考项目怎样解决跨渠道会话。本方案细化并更新[账号保护专项](2026-11-client-identity-account-protection-review.md)中的身份作用域设计。

后续用户明确要求客户端无感切换，已整合为[完整源码实施方案](2026-11-transparent-failover-source-plan.md)。该方案取代下文“首批不默认建立全量聊天内容存储”的阶段性建议：目标渠道使用加密 checkpoint，由网关恢复普通可迁移历史；客户端无须增加字段或手工新建对话。下文源码比较及不可恢复历史边界继续有效，尚未实施。

## 推荐结论

采用 **每渠道独立设备身份 + 网关内部逻辑会话 + 按渠道映射的上游会话 + 有条件的渠道切换**。

用户看到的同一段对话可以延续，但不同渠道收到不同的设备和 session/thread 标识。能否延续上下文由实际历史和资源归属决定，不能由 session ID 是否相同决定。

| 用户侧逻辑对话 | 渠道 | 设备/安装身份 | 上游会话 |
| --- | --- | --- | --- |
| 对话 L1 | A | A 的固定身份 | A-L1-分支1 |
| 对话 L1 故障后迁移 | B | B 的固定身份 | B-L1-分支2 |
| 另一段对话 L2 | A | 仍为 A 的固定身份 | A-L2-分支1 |

表中名称仅为示意，真实出站值使用不暴露渠道/用户/对话 ID 的不透明值，并遵循对应客户端字段格式。分支在连续使用期间保持稳定，不随每个请求变化。

不同设备 ID 能避免由网关主动复用该标识造成的关联，但不能隔离同一个真实账号、凭据或同一个中转后台已知的关联。真实 `account_id`、组织/项目 ID 不伪造；UA 版本、OS 等合法重复也不需要刻意全部不同。

## 参考项目实际怎样做

固定版本沿用上一轮报告；本轮重新读取会话、调度、续链实现与测试。以下为源码确认，不是这些项目生产防封效果的测试。

### Sub2API：账号命名空间、调用者隔离及 response 归属

- [openai_codex_account_identity.go][s-identity] 用上游账号身份或持久 seed 建立命名空间，再加入 API key、字段类型和原值派生身份。相同账号的重复本地记录共享命名空间；不同账号/调用者得到不同值。Header/body/嵌套 metadata 同步修改，选定候选后解析并覆盖 attempt 状态。
- [对应测试][s-identity-test] 直接覆盖两个账号同一个输入得到不同映射、相同账号重复生成一致、shadow 凭据及 failover 清理。它提供了“两层会话”的实用参考，但其默认单位是上游账号，不等于用户要求的 RenewAPI 渠道单位。
- [openai_ws_state_store.go][s-state] 保存 response→account、response→connection、session→connection，另有 HTTP response 的 user/API key 归属记录。[previous_response 调度][s-select] 尝试原账号，繁忙时给出有界等待计划；额度暂停时保留绑定，以便恢复后继续。
- 不能直接复制其所有降级：所读 state store 的部分缓存错误被当作未命中；[forward 恢复分支][s-forward] 在没有工具输出时可移除 previous_response_id 重试，并有删除失效 reasoning 的路径。这些条件本身不能证明完整历史可恢复。

**采用：**字段映射一致性、attempt 隔离、资源归属及短暂故障保留绑定。按用户要求将设备作用域适配成渠道身份空间；状态恢复另加完整性检查。

### CPA：粘连优先，避免在账号之间反复跳转

- [SessionAffinitySelector][p-selector] 支持 `any`、`quota`、`never` 三种重新绑定策略。`quota` 在短暂故障时保留绑定，在额度/认证/模型支持等指定问题下允许重选；`never` 不把已绑定会话转给其他凭据。默认仍是 `any`，不能误报为 CPA 默认严格粘连。
- 健康的既有绑定优先于账号 priority。[priority recovery 测试][p-priority] 覆盖 A 限额→切到 B→A 恢复后原会话仍留 B；新会话才重新优先选 A。
- [session identity][p-identity] 和 selector 优先使用明确客户端会话信号；缺失时才用派生内容根/LCP 匹配。内容相似是启发式线索，不应成为 RenewAPI 隔离或资源授权的依据。
- [Codex WS session][p-ws] 将连接与 authID、URL、proxy 等关联；绑定缓存解决选哪个账号，不代表服务端会话自动迁移成功。

**采用：**“健康时不换、短暂故障等待、指定故障才迁移、切换成功后不立即回跳”。加密/资源续链的强绑定优先于普通粘连策略，不允许被 `any` 覆盖。

### codex2api：本地路由标识与上游标识分开，缺少历史就明确失败

- [executor.go][c-executor] 的 `resolveUpstreamSessionID` 说明本地 affinity 使用独立来源；上游会话/缓存键按 API key 隔离。缺少明确会话时默认按请求隔离，不把同一个 key 下所有请求认作同一对话。其基础派生不是自动按账号隔离全部字段，仍需结合其他 fingerprint 配置，不能原样当作本方案完整实现。
- [Codex session headers][c-headers] 集中处理 `session-id` 与旧 `session_id` 的差异；二者不是同一个 HTTP header。保留 session/thread 区别，并明确激进收敛与缓存隔离可能冲突。
- [session affinity 测试][c-affinity] 绑定账号和 proxy；代理被删除后不得沿用失效出口，旧请求不能迟到写回已移除的绑定。
- [HTTP continuation 测试][c-http] 明确：仅有 `previous_response_id` 而本地没有父历史时，原生 Codex 路径返回上下文不可用，不能把新一条消息当完整历史；已有工具父历史则可重建调用与结果配对。即使输入里恰好有一组 call/output，也不能证明丢失的父上下文已经齐全。
- 对支持原生服务端存储的 Responses relay，该测试保留 previous_response_id 透传；这不能被解读为该 ID 可跨任意账号使用。
- [ResponsesContinuationLostError][c-continuation] 与 BusyError 分开，区分原 WS 状态丢失与原连接暂忙。[encrypted memory][c-encrypted] 只记录拒绝过的密文摘要，作用域含 owner/key/account/generation/session；删除已拒绝密文仍属于降级，不能当成无损迁移。

**采用：**独立路由键、正确会话头、账号/出口绑定、完整历史可重建才续接、失败有明确类型。首批不默认建立全量聊天内容存储。

### LiteLLM / NewAPI：补充边界，不替代上述方案

- [LiteLLM provider_affinity.py][l-affinity] 明确区分调用者会话与代理生成 trace/session；`get_caller_session_id` 不接受由 trace 提升的值。可配置 provider affinity header，但禁止使用 Authorization/Cookie/Host 等字段，并检查控制字符。[测试][l-tests] 覆盖这些边界。适合用于入口会话提取，不是跨账号历史迁移方案。
- [NewAPI channel_affinity.go][n-affinity] 提供可配置来源、会话→渠道缓存和 header 模板，思路与 RenewAPI 既有机制相近；没有理由用它替换本地已存在的 compaction 归属、带版本的粘连和 WS 隔离。仅针对所读模块得出这一结论，不表示审计过其所有插件。

## RenewAPI 的具体设计

### 1. 每个渠道独立且长期稳定的设备身份

- 渠道持久保存不可变的 `identity_scope_uuid` 与随机 seed；为 Codex/Claude 等客户端家族生成格式正确的 installation/device 标识。默认渠道间不共用 seed，进程重启、改渠道名、调整优先级均不改变。
- 单 key 渠道一份身份；多 key 渠道若包含不同真实账号，设备身份进一步按稳定凭据槽区分。槽位不能用会重排的列表下标标识；同一账号正常 token 刷新不换设备，新账号替换增加凭据代际。
- 复制渠道默认生成新身份；备份恢复原渠道保留身份。管理员设置的重复 ID 在保存/导入预览中指出，不静默生成相同值。
- 两条渠道若复用同一真实凭据，仍按用户选择保留独立设备配置，但凭据刷新锁、配额和终止错误按已确认的共享账号关联，不能靠不同 device ID 当成两个独立账号。此关联不自动授权两个渠道共享会话。
- 身份轮换只用于明确重置/更换账号等操作；不定时轮换“洗白”。受影响的旧会话保持原快照排空或显式重建，不能半途混用新旧字段。

### 2. 网关内部会话与上游会话分别记录

入口在任何身份改写前解析调用者的会话与线程；带上租户、API key 稳定身份和客户端协议作用域，得到内部逻辑会话 L。原始值及内部 L 不直接发给其他渠道。

出站 session/thread 由渠道 seed、凭据槽/代际、调用者作用域、L、真实 thread 和连续路由分支共同派生。相同分支重试保持不变；不同用户即使提交同一个原始 session ID，也不能映射成同一会话。Header/body/嵌套 metadata 共用同一结果。

`session`、`thread`、`turn`、请求幂等键、prompt cache key 分开建模；按具体客户端协议处理等值关系，不能一律赋成同一个 UUID。父子任务在同一逻辑会话树下保持不同 thread；内容相似、trace ID、相同 API key 不足以证明两个请求属于同一会话。prompt cache key 默认只是缓存/软粘连线索，只有明确客户端契约证明时才作为会话来源。

客户端完全不提供稳定会话标识时，返回可复用的网关会话标识供兼容客户端使用；未回传的请求不能承诺跨请求粘连。首批不依赖模型猜测或正文相似度来关联会话。

### 3. 两种绑定：路由偏好与资源归属

**软粘连**用于拥有完整、可重放历史的普通对话。首次选 A 后尽量继续 A；额度、故障和等待预算触发后才允许 B。B 成功后通过 CAS 更新路由版本，不因 A 恢复马上切回。迟到的 A 结果不得覆盖 B 的新绑定。

**强绑定**用于依赖上游私有状态的请求：previous_response_id、conversation/item/file 引用、加密 reasoning/compaction、Claude 不透明签名，以及依赖原 WS 连接的增量续链。记录实际来源渠道、凭据/项目、资源域、必要的连接身份和配置代际；不同 provider 能力下允许的共享范围必须有证据，不能只因两条渠道同 URL 或同模型就认为可互换。

资源归属表与普通粘连 TTL 分开：缓存驱逐、重启或查询失败不能把强绑定自动降为任意路由。当前生产没有 Redis，必要的归属/路由代际用 SQL 持久化，Redis 只能加速。保存最小归属与摘要，不因此默认保存全部提示词。每次资源 lookup 验证租户/API key 权限，猜到 response ID 不等于拥有它。

### 4. 渠道切换策略

| 当前请求 | A 不可用时 |
| --- | --- |
| 完整可重放的文本/多模态/工具历史，目标 B 支持同等语义 | 有界等待后，在 B 新建上游分支，投递完整必要历史，使用 B 的独立身份；保留工具调用与结果配对，重新计算 token/费用 |
| 只给 previous_response_id 或 item 引用，缺父历史 | 保持 A 的归属；短暂不可用返回可重试提示，无法恢复则要求提供完整历史或开启新分支，不删除引用后装作续聊成功 |
| 加密思考、压缩密文、签名块或仅原 WS 连接持有的状态 | 默认强绑定；仅在明确支持的同状态域内续接。无法续接时给出上下文不可迁移错误 |
| 用户明确选择“用可见历史/摘要重建” | 在 B 新建分支，说明这是上下文降级；只用实际取得的内容，不声称保留隐藏思考/原缓存；需要时由客户端重发完整历史 |
| 已向客户端输出，或工具执行/上游结果是否完成不明确 | 不在同一请求中换渠道重放；保留终态与调用 ID，交由客户端恢复，避免重复执行或重复计费 |

“有 input/messages 数组”不等于完整历史。客户端显式声明全历史、可验证的历史快照或受支持的恢复协议才可作为重建依据；状态不确定时采用强绑定。即使重新发送可见历史，目标模型的续写也可能不同，不能承诺逐 token 等价。

L1 已从 A→B 并新增对话后，将来再次回 A 不能直接续用 A 旧分支的 response ID。应在 A 建立新的路由分支并使用完整可重放历史；不能把 B 上的新增历史假定为 A 已知。

对现有中转渠道，RenewAPI 只能固定中转渠道/凭据/传入标识。若中转内部仍随意轮换上游账号，需要其提供并遵守粘连约定，否则本地强绑定只能保证到中转这一跳。

## 本地已有能力与新发现

当前 main `b7788ff39176337f02b5067ca96a85afa2715584`：

- [channel_affinity.go](../../service/channel_affinity.go) 有带 Generation 的 ChannelAffinityRecord、CAS 清理、compaction 摘要归属、multi-key 偏好和有限恢复预算；应该扩展而非重写。
- [responses_websocket.go](../../relay/channel/responses_websocket.go) 按下游连接/lane 隔离，并将渠道、URL、header/auth、代理/TLS 配置纳入握手范围；前缀确实一致才做 previous_response 增量。这部分保留。
- [session_route_recovery.go](../../service/session_route_recovery.go) 的会话修复分支会识别 state-bound，不给加密/previous_response 请求做普通 failover。
- 但[controller/relay.go](../../controller/relay.go) 的另一条 `prepareEncryptedReasoningRetryBody` 兼容路径，在跨渠道时可调用 [ScrubEncryptedReasoningRequest](../../service/encrypted_reasoning_compat.go)，删除 previous_response 和 reasoning。所读 helper 本身不重建缺失父历史；“会话修复分支不切换”不代表整个转发链都强绑定。
- 2026-11-15 Playwright 只读确认线上 `channel_affinity_setting.enabled=true`、默认 TTL 3600、`switch_on_success=true`；`encrypted_reasoning_fallback_setting.affinity_enabled=true`、`scrub_fallback_enabled=true`、`max_fallback_times=10`。这是配置值，不代表每次会发送十个上游请求，其他预算仍约束实际次数。
- 本轮合成 `session_recovery_repro.go` 调用真实 scrub helper：仅提供 synthetic previous_response_id 和“继续之前任务”这一条 input，得到 `previous_reference_removed=true`、`input_unchanged_without_parent_reconstruction=true`。只证明该函数会清引用而未补父上下文，没有重放生产请求或证明某条生产对话已丢失。

因此新增一个优先修复项：**把资源归属和历史可迁移检查放在所有入口、重试、模型 fallback、同渠道换 key、HTTP/WS 切换之前，统一决策；现有 scrub 只能成为明确声明的降级，不能旁路强绑定。** 不直接全局关闭线上开关，本轮只确定设计。

## 产品与验收

渠道面板显示设备/安装身份摘要、是否自动生成、配置代际和冲突检测；提供显式重置及迁移预览。路由策略建议两个常用模式：“优先保持，能恢复才切换”（默认）与“固定渠道”；另将“允许可见历史降级重建”设为独立 opt-in，不混进设备身份开关。

首批完成设备作用域、会话映射、持久归属、统一迁移判定和诊断。完整历史托管/摘要恢复如需要，应单列存储期限、隐私和计费设计，不为实现设备隔离默认新增聊天档案。

验收至少覆盖：同渠道稳定/跨渠道不同、同原始 session 的不同用户隔离、不同真实会话不合并、复制渠道不继承身份、同账号 token 刷新稳定、多 key 重排、A→B→A 分支不串、父子 thread、旧请求迟到 CAS、代理变更、进程重启/缓存故障、缺父历史、签名/密文/文件引用、已输出及工具副作用不重放。证明隔离与恢复正确，不宣称由此保证不封禁。

本轮实际运行并通过：

```text
go run D:\Code\renewapi\audit\20261115-client-risk-source-review\session_recovery_repro.go
go test ./service ./controller -run '^(TestDecideSessionRecovery|TestSessionRecoveryBudget|TestScrubEncryptedReasoningRequest|TestResponsesWSNativeReuseContinuationIdleReaderAndScope)' -count=1
```

上游测试只读未执行；本地测试验证既有行为，不代表新设计已经实现。源码快照和补充 LiteLLM 两文件位于上一轮审计目录，产品代码、生产配置和部署未变。

[s-identity]: https://github.com/Wei-Shaw/sub2api/blob/530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484/backend/internal/service/openai_codex_account_identity.go
[s-identity-test]: https://github.com/Wei-Shaw/sub2api/blob/530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484/backend/internal/service/openai_codex_account_identity_test.go
[s-state]: https://github.com/Wei-Shaw/sub2api/blob/530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484/backend/internal/service/openai_ws_state_store.go
[s-select]: https://github.com/Wei-Shaw/sub2api/blob/530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484/backend/internal/service/openai_ws_forwarder_support.go
[s-forward]: https://github.com/Wei-Shaw/sub2api/blob/530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484/backend/internal/service/openai_gateway_forward.go
[p-selector]: https://github.com/router-for-me/CLIProxyAPI/blob/e96d4c4818851cf0e96d27b2bc8b67379e449d9e/sdk/cliproxy/auth/selector.go
[p-priority]: https://github.com/router-for-me/CLIProxyAPI/blob/e96d4c4818851cf0e96d27b2bc8b67379e449d9e/sdk/cliproxy/auth/session_affinity_priority_test.go
[p-identity]: https://github.com/router-for-me/CLIProxyAPI/blob/e96d4c4818851cf0e96d27b2bc8b67379e449d9e/sdk/cliproxy/session/identity.go
[p-ws]: https://github.com/router-for-me/CLIProxyAPI/blob/e96d4c4818851cf0e96d27b2bc8b67379e449d9e/internal/runtime/executor/codex_websockets_session.go
[c-executor]: https://github.com/james-6-23/codex2api/blob/41e8d86bf0d4eedee5322e7668af595df4142658/proxy/executor.go
[c-headers]: https://github.com/james-6-23/codex2api/blob/41e8d86bf0d4eedee5322e7668af595df4142658/proxy/codex_session_headers.go
[c-affinity]: https://github.com/james-6-23/codex2api/blob/41e8d86bf0d4eedee5322e7668af595df4142658/auth/session_affinity_test.go
[c-http]: https://github.com/james-6-23/codex2api/blob/41e8d86bf0d4eedee5322e7668af595df4142658/proxy/response_cache_http_continuation_test.go
[c-continuation]: https://github.com/james-6-23/codex2api/blob/41e8d86bf0d4eedee5322e7668af595df4142658/proxy/responses_continuation.go
[c-encrypted]: https://github.com/james-6-23/codex2api/blob/41e8d86bf0d4eedee5322e7668af595df4142658/proxy/encrypted_content_memory.go
[l-affinity]: https://github.com/BerriAI/litellm/blob/e38187324ad3b49e8111c6823a8cfb3c7d6c2f7d/litellm/litellm_core_utils/provider_affinity.py
[l-tests]: https://github.com/BerriAI/litellm/blob/e38187324ad3b49e8111c6823a8cfb3c7d6c2f7d/tests/unit/litellm_core_utils/test_provider_affinity.py
[n-affinity]: https://github.com/QuantumNous/new-api/blob/b388140acfd80369ec6243b16869daff1b4051f4/service/channel_affinity.go
