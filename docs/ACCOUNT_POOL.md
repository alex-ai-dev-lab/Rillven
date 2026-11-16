# 共享订阅账号池

基础源码实现于 2026-11-16，未发布、未部署。验收范围见
[验收记录](account-pool-validation-20261116.md)，设计约束见 ADR-025。

## 配置与账号范围

复用现有渠道、分组、成员/Token 授权和模型路由表达账号池，不另建第二套权限或计费系统。
普通成员使用自己的 API Key；API Key 的轮转不会自动继承旧 owner 的历史。

仅为需要共享的渠道设置 `client_identity_mode=per_account`。恢复需设置
`conversation_recovery=managed` 并配置独立 `RILLVEN_STATE_KEYS` 和
`RILLVEN_STATE_ACTIVE_KEY`；旧 `RENEWAPI_STATE_*` 别名保留。所有实例使用相同
数据库及完整 keyring。旧 off/shadow/managed、per_channel/legacy/disabled
和出口策略继续按原契约读取。现有渠道不自动切换为新模式。

- Codex 用账号/workspace 和 issuer/principal 归一认证主体；邮箱仅是旧导入的兼容线索。
  JWT 解码不等于签名认证；凭据来自管理员导入，真实上游认证另行验收。
- 不透明凭据首次建立随机持久 credential 主体，旧 grant alias 在轮转后保留。
  不同签发的 opaque key 需要管理员明确设置 `upstream_account_subject` 才能归为同一账号。
- 新账号首次生成 UUIDv4 安装 ID；旧渠道的已生效安装身份可保留。同主体的旧安装身份
  不一致时拒绝自动合并，单账号渠道可通过 `upstream_account_id` 明确采用主身份。
  多账号旧渠道的逐账号冲突需先拆分/核实绑定，不自动选取一个身份覆盖所有账号。
- per_account 凭据写入逐 grant 加密 vault，渠道保存引用。全局继承该模式的旧明文渠道
  首次受保护读取时回填，不提升配置版本。该契约不意味着全站所有旧 API Key 均已迁入 vault。

共享账号自动启用 SQL 准入；原并发、成员/Token 限流、模型授权和资金账本仍生效。
`RILLVEN_UPSTREAM_CONCURRENCY` 默认 4，`RILLVEN_UPSTREAM_QUEUE` 默认 32，
`RILLVEN_UPSTREAM_WAIT_MS` 默认 3000。FIFO 按请求到达顺序协调，不是成员加权公平调度。
丢失在途 lease 后取消本地传输，并保留 2 分钟容量隔离期。

## 身份生成与升级

固定官方 Codex 源码 `662a634e88fcc809d77da393d75572c3d814cb27`，适配官方
model-facing ID 的 BLAKE3、四轮 Feistel、cycle walking 和三位十进制分组逻辑；
保留版本、ordinal、value 和持久 sequence。UUIDv7 用于线程、turn、context 的首次分配。
这两种生成器有不同用途，不能将模型可见数字 ID 直接替代所有协议 UUID。

安装身份属于账号；session/thread/window/父子关系属于账号和 owner 范围。普通消息、
代码和工具文本中的 ID 不做扫描替换。HTTP、原生 WS、compact、额度和 OAuth 维护
复用实际出站解析器；模板先执行，受保护身份和认证最后校验。

客户端版本、平台和终端在首次登记时固定。渠道配置与固定画像冲突时，实际请求拒绝；
管理员预览可显示现有画像，并显式执行“将已保存的渠道客户端画像应用到此账号”。
此动作只提升 profile/记录 revision，不重置 installation、device 或线程映射。
身份重置使用独立 identity revision，并影响使用该账号的所有渠道；画像或身份发生变化后，
下个 WS turn 检查作用域并重新握手。配置保存和画像升级都需要旧 revision，过期表单不能覆盖新值。

## 额度与历史

默认 Codex primary/secondary 额度读取实际响应头、`codex.rate_limits` 事件和 WHAM。
未知额度保留未知；观察窗口耗尽后直到 reset 都不以迟到成功恢复可用性。reset 后使用单并发
半开准入，等待新观察。共享 WHAM probe 使用数据库时钟和账号 lease，默认 60 秒目标周期
加 0–10 秒抖动，扫描粒度 15 秒；可通过 `RILLVEN_QUOTA_REFRESH_SECONDS` 调整，范围 30–3600。
模型专属 metered family 不混入默认额度池。调度不保证未配置的供应商/模型额度已被识别。

已知托管响应通过不可变父链、加密 checkpoint、资源 registry 和账本 receipt 恢复。
消息及客户端 function/custom 工具结果可以完整迁移；工具 call_id、namespace、参数数字精度
和配对不变。推理密文、私有 file/container、未知资源/前像或有状态服务端工具不会靠删字段切号。
引用必须有 owner 和账号归属，猜中上游 ID 不授予权限。

`previous_response_id` 默认携带增量；完整前缀完全一致时可识别为完整历史。
`X-Rillven-History-Mode: full` 要求完整前缀证明；部分前缀/已编辑历史拒绝静默 append。
`delta` 明确表示追加，允许有意重复普通用户消息。该规则仅用于已登记账号历史，旧 per_channel
历史保持原读取契约。输入配置的 `upstream_context_tokens` 为 0 时由 provider 检查；非零时
按估算输入和显式输出预算拒绝超窗，不截断上下文，不自动运行目标端 compact。

HTTP `Idempotency-Key` 或 WS 每个 `response.create` 的 `idempotency_key` 在 owner 范围内
持久认领。相同内容重取已发布结果，不再次生成或扣费；同 key 不同内容返回 409。
尚未确认、删除、过期或没有可重取结果的 claim 返回 409 并保留墓碑。流式重取返回包含完整
output 的终态事件，不重演原 delta 序列。未提供稳定幂等键的人工重发不承诺 exactly-once。

## 删除、密钥轮转与恢复

成员接口均使用正常 Token 鉴权：

- `GET /v1/rillven/conversations`：仅列当前 owner 的未删除会话。
- `DELETE /v1/rillven/conversations/:id`：先持久阻止访问；在途/未结算 turn 返回 202，
  后台等待 lease/结算完成再清理正文。已清理返回 204；其他 owner 返回 404。
- `DELETE /v1/rillven/conversations`：每批请求删除最多 128 条当前 owner 会话，重复至 requested=0。
  这包括当前成员当前 Key 的 owner；不扩大到该成员其他 Key 的历史。

删除记录不包含正文；清理删除 checkpoint、资源、别名、前像和结果副本，保留必要的资金记录、
幂等 claim 和 refresh grant 墓碑。删除不能撤回已经交付的输出，不能立即取消另一实例已经发出的上游操作。

Root 管理接口：

- `POST /api/channel/relay_state/reencrypt?limit=128`：对身份、credential、received refresh
  journal、checkpoint 做密文 CAS 重加密，重复至 updated=0。失败或存在引用前不要删除旧 key。
- `GET /api/channel/relay_state/deletions?after=<id>`：按 ID 升序每批导出最多 256 条删除记录。
- `POST /api/channel/relay_state/deletions`：导入同格式 `{ "data": [...] }`，每批 1–256 条。
  在恢复流量之前应用独立保存的最新删除记录，避免旧快照恢复已删除的会话。

备份必须包含一致数据库快照、全部身份/credential/关系分配、journal、checkpoint/receipt、
配置、schema 版本和全部在用密钥；密钥单独保管。删除记录应独立于旧数据库快照持续保存。
离线旧备份有自身保留期，在线删除不表示所有旧备份已即时销毁。

迁移增加新表/字段，保留 `conversation-state:v1` 的旧 revision/checksum，使用新的 additive
migration。旧 AAD 可读，新 account AAD 不冒充旧记录。回退必须使用认识 vault、新账号字段
和新 logical ID 的兼容版本；不能直接把写入新状态的数据交给旧 v1.6.0。OAuth 轮转和资金状态
也不能靠恢复旧备份倒退。

## 验收限制

基础出站保留 Go HTTP/gorilla WS。固定 UA、官方生成算法和本地 HTTP/WS 捕获均不能证明
TLS/ALPN/H2 字节画像完全等同官方客户端。`codex_native`、自动目标 compact、私有资源迁移
仍是独立可选能力；真实账号/供应商额度、真实双进程流量和传输对照尚未验收。
