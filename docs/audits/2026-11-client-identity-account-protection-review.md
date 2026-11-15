# 2026-11-15 客户端身份、账号保护与上游源码复核

状态：方案完成，尚未实施。用户要求扩大源码审阅范围，优先考虑防风控、防封禁和客户端伪装；本报告调整[原整体方案](2026-11-pricing-tools-upstream-plan.md)的实施顺序。产品代码、生产配置和部署均未修改。

最新整合：[无感切换源码实施方案](2026-11-transparent-failover-source-plan.md)将每渠道身份、托管历史、两项原始 bug 和账号保护串成 P0–P5 提交。用户要求普通客户端无需手工重建历史，因此其存储/迁移及实施顺序以该方案为准；本报告作为上游行为和现状证据保留。

后续澄清：用户明确设备/安装 ID 应按渠道独立配置，并要求进一步参考上游的跨渠道会话方案。已补充[每渠道独立身份与会话设计](2026-11-channel-identity-session-design.md)：默认设备作用域以渠道为单位，多真实凭据再细分；内部逻辑会话与上游会话分开，资源续链强绑定，完整历史才可迁移。该文同时记录新发现的本地 scrub 兼容路径与只读生产设置，作为本报告身份方案的进一步细化。

## 结论与线上适用范围

优先建设**按凭据和会话隔离的客户端身份、统一出站配置、准确的错误退避和状态恢复**。将客户端兼容配置做成显式、可观察、可回滚的渠道能力；TLS 指纹放在可验证的后续试点。源码能够证明某项机制如何工作，不能证明它降低了模型厂商封禁率。

本轮通过 Playwright 和 SSH 只读 SQLite 查询，确认：

| 当前生产事实 | 对方案的影响 |
| --- | --- |
| 37 条启用渠道：13 条 OpenAI 类型、24 条 Anthropic 类型；地址均为自定义主机，无原生 Codex 类型 57 渠道 | 当前收益主要在中转兼容、身份隔离、限流和会话连续性。自定义地址背后的实际账号池未知；不能把 OAuth 修复描述为当前线上故障原因。 |
| `client_identity_setting.enabled=true`，全部 Responses/Messages 均应用；Codex `force_global`，Claude 设备及 session 都是 `force_global`；轮换关闭 | 多个调用者会被重写成相同设备，Claude 多个会话会被重写成同一 session。这是已确认行为，尚无证据证明它导致了封禁。 |
| OpenAI/Claude 各有一条启用的全局 UA；25 条启用渠道显式配置代理，4 条启用渠道关闭 TLS 证书校验 | 需要显示实际生效配置及覆盖来源。未配置渠道代理不等于直连，现有 HTTP 客户端还会读取环境代理；真实出口未在本轮抓包验证。证书配置应逐渠道诊断，不突然全局切换造成中断。 |
| 线上定价保存不可达，以及 namespace 工具本地预检 400，已有独立复现 | 保留最高优先级。这两项不能靠 UA、TLS 或换账号修好。 |

TLS 指纹影响 RenewAPI 与下一跳的 TLS 握手。若下一跳是中转服务，其访问模型厂商时使用的 TLS、OAuth、账号和出口由中转服务决定。请求头及 metadata 是否被保留也取决于下一跳实现。这是当前“防封禁”优化的实际控制边界。

## 实际审阅范围与证据等级

下载 Sub2API、NewAPI、CPA、codex2api 的完整固定 SHA 快照，分别包含 4,648 / 2,832 / 2,382 / 1,918 个文件；按目录树选择 LiteLLM 的 35 个源文件和测试。另增加官方 `openai/codex` 的四个请求/身份源文件作为客户端基准。**下载文件数不等于逐文件审计数**；本轮深入读取下述模块、调用点和相关测试，没有运行上游完整测试套件。

| 项目 | 固定源码版本 | 重点实际读取 |
| --- | --- | --- |
| Sub2API | `530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484` | Codex fingerprint、account identity、outbound identity；forward 调用点；OAuth refresh API/policy/service；TLS dialer；上游传输错误分类 |
| CPA | `e96d4c4818851cf0e96d27b2bc8b67379e449d9e` | Codex refresh journal/patch/结果未知测试；Claude credential identity/CLI seed/profile；uTLS；Antigravity TOS parser；cooldown/concurrency 配置 |
| codex2api（用户指定 james-6-23） | `41e8d86bf0d4eedee5322e7668af595df4142658` | OAuth lease；Codex outbound/maintenance identity、device profile、fingerprint modes；Claude 指纹与隔离测试；账号绑定出口、uTLS；遥测出口；额度帧和冷却持久化 |
| NewAPI | `b388140acfd80369ec6243b16869daff1b4051f4` | 原生 Codex refresh、模型发现；渠道禁用/恢复；手工禁用保护测试；新增 provider plugin 的 needs-login 测试路径 |
| LiteLLM | `e38187324ad3b49e8111c6823a8cfb3c7d6c2f7d` | ChatGPT authenticator/common utils、Codex passthrough/version；cooldown handlers；Anthropic beta manager；相关请求/响应与测试文件 |
| 官方 Codex | `b7056f3408522716b6643c8b3e23ce7c9b53b041` | `login/src/auth/default_client.rs`、`codex-api/src/requests/headers.rs`、Responses HTTP 和 WS connector |

证据区分：**生产实测**为只读配置/布局/既有日志；**本地复现**调用 RenewAPI 当前真实函数；**源码确认**包含最终实现及适用边界；上游源码中的封禁、额度下降或某 UA 风险解释仅为**上游报告/推测**。

对五仓的 21 个关键路径额外读取了 2026-10-17 以来最多五条提交历史，保存在 `path-histories.json`。没有近期结果的路径作为现有机制参考，不包装成“本月新增”；路径更名及更早历史未据此排除。查询时最新 Release 见原方案，固定 HEAD 不等于对应 Release 内容。

本地证据目录：`D:/Code/renewapi/audit/20261115-client-risk-source-review/`。包含固定源码、目录树、路径历史、只读脱敏生产摘要、采集脚本和 `identity_repro.go`。没有在材料中保存生产令牌、密码或实际设备标识。

## 一、身份和会话：当前最应该改的部分

RenewAPI 的 [model/client_identity.go](../../model/client_identity.go) 默认全局强制覆盖；[service/client_identity.go](../../service/client_identity.go) 接收 path/headers/body，没有凭据或调用者作用域。当前生产也使用这套配置。

本地合成两个不同客户端调用真实函数，确认：Codex installation ID 相同；Claude `metadata.user_id` 中设备/session 相同；`X-Claude-Code-Session-Id` 相同。现有测试明确验证的是强制覆盖行为，所以测试通过并不代表隔离已经存在。

### 值得吸收的实现

- [Sub2API account identity][s-account] 使用稳定的凭据命名空间，再区分下游 API key、字段种类和原始会话值；重复导入同一上游凭据不应因数据库行不同而成为完全不同的身份。Header/body/嵌套 metadata 使用一致派生。其 [fingerprint][s-fp] 暂存值带 account ID，failover 即使切到 off 账号也覆盖为 nil，避免沿用前一次尝试的身份。
- Sub2API 的 device/session/full 模式默认 **off**。源码记录 v0.1.175 曾默认 session，用户在 #5555/#5556/#5582 报告额度下降，随后 #5610 恢复显式开启。这是反对直接采用激进默认值的证据，不是已证实的风控因果关系。
- [codex2api Claude 隔离测试][c-claude-test] 区分 header、metadata session 与缓存键来源；同 key 稳定、不同 key 隔离；保留业务 `metadata.user_id` 和不同的父会话字段。[Codex 模式][c-modes] 也默认 off，中转型账号强制 off；另有“同设备、多窗口”模式，不能混同于全会话合并。
- [CPA Claude profile][p-profile] 默认调用者拥有身份，`claude-code-cli` 显式开启；[身份 seed][p-seed] 使用稳定凭据身份，真实 OAuth 不通过虚构字段掩盖缺失的账号/设备信息。对共享对象先 clone 再补齐，防止并发污染。
- [官方 Codex][o-headers] 独立构建 `session-id` 和 `thread-id`，按真实子任务来源设置相应字段；没有依据把全站不同对话合成一个线程。

### RenewAPI 方案

1. 新增身份作用域：渠道持久身份空间 → 稳定凭据槽/代际 → 下游租户/API key → 会话/线程。设备 seed 属于渠道/凭据槽，不按用户或请求轮换，不依赖短期 access token；迁移/重启后稳定。重复真实凭据的刷新锁/额度按凭据族关联，但不自动合并渠道设备或会话。中转渠道不知道真实上游账号时，仅按自己掌握的渠道凭据隔离，不伪称已经管理中转背后的账号。
2. 提供 `preserve`、`fill_missing`、`credential_stable` 等清楚模式，并单独选择 Codex/Claude 兼容配置。设备稳定与会话合并拆开；默认不合并不同真实会话，不每次请求随机换设备。
3. 在选定候选渠道后生成本次尝试的身份快照，HTTP、WS、body/header 共用；失败切换重新解析，禁止残留。缓存分区、session、thread 保持各自语义。
4. 保留业务 metadata，识别 CLI 身份结构后再改；继承 ADR-017/018/022 的工具、签名及加密历史边界，不把“伪装客户端”变成删除参数或改写用户提示词。
5. 原全局配置作为 legacy 保留并显示迁移差异；小批渠道和新会话切换，避免升级一瞬间改变所有活跃会话标识。旧会话有限期沿用原快照，过期后显式重建。

## 二、客户端兼容配置：UA、版本和协议必须一起管理

RenewAPI 已有渠道 UA 覆盖、全局分类 UA、Codex request profile 和 metadata 注入，不能重新做一套互相竞争的“伪装开关”。当前实现中，推理 UA 在 [api_request.go](../../relay/channel/api_request.go) 解析；Codex adaptor 和用量查询分别设置 originator；OAuth 刷新只显式写 Content-Type/Accept。这些路径缺少统一解析入口。

值得吸收：

- [Sub2API outbound identity][s-identity] 同源解析 UA/originator/version；认证面只使用适用的字段，不把推理用 `version` 一律加到认证端点。近期 [c591412c42（10-22）](https://github.com/Wei-Shaw/sub2api/commit/c591412c42ab195dacdede9eb19f9cbfee221144) 涉及身份与空流重试。
- [codex2api outbound/maintenance resolver][c-identity] 让对话、用量、订阅及模型发现共用解析，最后应用账号覆盖；[4256e36d64（11-01）](https://github.com/james-6-23/codex2api/commit/4256e36d6410f1109f4d23c42deb32cab27626fc) 统一维护身份，[246163817e（11-05）](https://github.com/james-6-23/codex2api/commit/246163817e365c714ff940fffc83149455bb7bcd) 修正设备稳定化覆盖强制 UA。
- [官方默认客户端][o-client] 从 originator、实际构建版本、OS/架构和终端构建 UA；[WS connector][o-ws] 合并请求头后单独应用认证。这说明应区分身份字段与认证字段，不能靠只改 UA 完成完整兼容。
- [LiteLLM][l-common] 集中生成 originator/UA，session 优先使用已有会话来源，后备缓存键需保证 header 安全；[d9aae590e3（10-28）](https://github.com/BerriAI/litellm/commit/d9aae590e344625a86cdccea2b853f8fda709fa9) 补了 prompt_cache_key 会话来源。此来源也需要 RenewAPI 自己的租户隔离，不能原样全局共用。

建议统一成 `OutboundClientProfile`（设计名）：客户端家族、兼容版本、设备 seed、字段来源、可用能力、网络配置版本。具体认证信息仍独立。每个端点定义允许发送的字段，覆盖 inference、compact、WS、models、usage、OAuth；版本更新先验证再晋级，支持固定版本和上一可用版本，不能后台抓到新版就改变所有账号。

Claude 的 UA、Stainless SDK/runtime、OS/arch、beta 和 metadata 应来自一套经过验证的兼容组合。codex2api 的 [ClaudeFingerprint][c-claude] 会分别从 SDK/runtime 池取值；“各字段合法”不足以证明组合来自真实客户端，不直接复制随机组合。LiteLLM 的 [beta manager][l-beta] 适合参考按供应商映射能力，但不照搬静默过滤所有未知 beta，避免重现工具扩展被丢弃的问题。

## 三、网络出口与 TLS：保留现有保护，做可选传输能力

RenewAPI 已有代理、环境代理、HTTP1/H2、分片连接池、SSRF 和长流生命周期保护。本地合成验证 `unsupported-proxy://...` 会返回错误，不能移植会降低此保障的实现。

具体源码取舍：

| 来源 | 实际行为与边界 | 处置 |
| --- | --- | --- |
| [Sub2API TLS dialer][s-tls] | 自定义 Node/Claude 风格 ClientHello；源码提供 cipher/extension/ALPN 配置。其 HTTPProxyDialer 对 `https` 只切换默认端口，所读实现随后直接在 TCP 上写 CONNECT，未先与代理建立 TLS | 借鉴按客户端族配置的接口；不复制这个 HTTPS 代理实现。属于源码确认，未对上游部署实测。 |
| [codex2api uTLS][c-tls] / [auth uTLS][c-auth-tls] | 旧构造器代理解析失败会退回 direct；HTTPS 代理实现已经在 CONNECT 前增加 TLS | HTTPS 代理修正值得参考；静默直连不采用。 |
| [codex2api account-bound egress][c-egress] | 新的显式账号绑定出口在进入旧构造器前验证，错误直接拒绝；空代理是明确直连，有测试证明不继承环境/Resin | 应采用这种“出口配置快照”的契约。不能把旧构造器缺陷概括成该项目所有路径都会直连。 |
| [CPA uTLS][p-tls] | 同样有旧构造器代理错误仅记录日志的分支；当前默认每次请求独立连接并随 body 关闭 | 不整段复制。11-08 的关闭默认复用变更有其特定背景，不能覆盖 RenewAPI 已验收的 H2 长流池。 |
| [codex2api telemetry transport][c-telemetry] | 遥测复用账号出口；带凭据分析请求禁重定向，metrics 与 analytics 鉴权不同 | 吸收出口一致及凭据不随重定向泄漏的原则；没有必要为“养号”制造遥测事件。 |

实施内容：显式区分 inherit/direct/proxy；每个请求冻结出口配置，维护流量走同一策略；错误不降级直连。连接池 key 纳入代理策略、TLS profile/版本及必要的凭据隔离，修改配置只退役对应池且不截断在途长流。

TLS 试点选择经过实测的客户端族配置，保持证书/SNI/hostname 校验，测试 ALPN、H2 设置、代理 CONNECT 和 WS。JA3/JA4 相同只代表握手特征近似，不证明完整客户端相同，更不证明不会封禁。对当前自定义中转地址，先证明存在兼容收益再开启；不因“Chrome 更像浏览器”就全局替换 Go/Rust/Node 客户端行为。

## 四、OAuth：需要单一刷新所有者和结果记录

这是原生账号接入的高价值前置改造，当前 37 条启用渠道没有走 RenewAPI 原生 Codex 刷新路径。

本地 [codex_credential_refresh.go](../../service/codex_credential_refresh.go) 是读 channel → 刷新 → `WHERE id` 覆盖 key，没有凭据族互斥/CAS；10 秒 context 继承调用者取消。[codex_oauth.go](../../service/codex_oauth.go) 没有区分未发送、已发送结果未知和终止型刷新失败。后台任务的运行标记不能覆盖管理员与后台并发，也不能跨实例保护。

最值得适配的是组合方案：

- [CPA journal][p-journal] 在消费 rotating grant 前申请权威 claim，以 clientID+token 的摘要定位；成功结果写 receipt，后来调用者读取结果链；已发送但结果未知不自动重放。独立、有限时长 context 保存结果；[patch][p-patch] 按原始凭据值检查更新，不覆盖并发修改的其他配置。
- 更新依据：[68cd75724e（11-06）](https://github.com/router-for-me/CLIProxyAPI/commit/68cd75724e4de4e0450475cc99c7491166cf3392) 防止未知结果重放；[1f5bf2a569（11-11）](https://github.com/router-for-me/CLIProxyAPI/commit/1f5bf2a569c35eec45637836c440954c5cf1ddae) 允许确定未发送时释放 claim。读取其 [outcome tests][p-outcome]，包含丢失响应、跨调用、307 重定向和 429/503 不证明可重放的案例。
- [codex2api lease][c-lease] 将刷新和 CAS 落库放在同一临界区；共享缓存出错时不降级成本地锁，调用者取消不丢掉已轮换结果。源码明确旧二进制不理解新 fence，滚动升级仍要停止旧刷新者。
- [Sub2API OAuthRefreshAPI][s-refresh] 有本地锁、分布式锁和锁后重读，但 Redis 错误分支仍继续本地保护下刷新；多实例 rotating grant 场景不采用该降级行为。
- NewAPI 的[原生 Codex 刷新][n-refresh]仍接近本地 `WHERE id` 写回，不能视为该问题已经有现成上游修复。其新增插件路径的 needs-login 状态值得单独适配，无需引入整个插件市场。

RenewAPI 方案：用现有 SQL 支持的唯一 claim/凭据版本构建持久刷新状态，Redis 可作协调加速但不能替代权威结果。状态包括 idle、claimed、succeeded、rejected、outcome_unknown；“有锁 + CAS”仍不足以应对上游成功但本地崩溃，必须记录不确定状态。receipt 含敏感凭据，沿用凭据存储权限和保密边界，不写日志。对不确定结果提示重新授权/人工核对；明确失效、吊销、重用停止刷新，不周期性打失效 token。

## 五、错误分类、并发和额度：减少无效重试与误封渠道

“防封禁”中可以直接验证的收益是减少无效请求、避免把一条凭据的异常扩散到其他凭据，以及不在额度窗口未恢复时反复调用。

| 情况 | 建议作用域及行为 |
| --- | --- |
| 本地协议不可表达、非法工具、调用者取消 | 本次请求；不归因于上游账号，不以改身份反复重试。沿用既有协议/加密历史例外。 |
| access token 到期 | 仅拥有该 OAuth 凭据的路径尝试一次协调刷新；中转 API key 的 401 不自动当作 OpenAI OAuth。 |
| revoked / refresh_token_reused / invalid_grant | 对应凭据 needs-login，停止刷新与自动探活恢复。 |
| 403 无模型权限、账号暂停、结构化 TOS_VIOLATION | 分类别处理；已确认暂停隔离该凭据，普通模型无权限只排除相应能力，不能所有 403 都全池封停。 |
| 429 速率 / 窗口额度 / 临时容量不足 | 分开统计与调度，尊重 Retry-After/reset；凭据或额度组共享冷却；排队和重试次数有上限及抖动。 |
| response/thread 不属于该凭据，或加密历史不匹配 | 会话/资源归属错误；不能据此判断模型离线，也不能换账号无限重试。 |
| DNS、代理认证、连接失败 | 网络/出口故障；在确认为持续故障时暂停相应路径，不改写为账号封禁。 |

参考源码：

- [LiteLLM cooldown handlers][l-cooldown] 跳过调用者专属凭据错误、本地 malformed/unsupported endpoint，并区分已有 response/video/thread 的 404；借鉴归因方式，不复制整个 Python router。
- [CPA Antigravity TOS][p-tos] 精确要求 403/PERMISSION_DENIED/ErrorInfo 的 `TOS_VIOLATION`，通过错误状态让调度层停用凭据；11-13 加入，11-14 最新提交已将全池暂停改为默认关闭。适合借鉴“终止错误有明确证据和范围”，不把 Antigravity 专用解析套给所有厂商。
- [Sub2API refresh service][s-refresh-service] 区分账号终止错误与 provider 配置错误，并有 provider 级刷新 QPS/并发控制；[transport errors][s-transport] 区分持久网络问题与客户端取消。
- [codex2api usage cooldown persistence][c-cooldown] 冷却设置和清除都可重试持久化；失败的 ClearError 不能退化成 ClearCooldown。最新 [df3a72f712（11-11）](https://github.com/james-6-23/codex2api/commit/df3a72f712a0f59a2851bc64c4fddcee819de9bc) 防止旧 7 天冷却复活。[FIFO 测试][c-fifo] 保留部分 5h/7d/credits 帧顺序，不能只保留最后一个覆盖前面窗口。
- [NewAPI 手工禁用测试][n-status] 覆盖单 key、多 key、内存缓存开关，不把手工禁用改成自动禁用；[插件 needs-login 测试][n-plugin-test] 仅停掉失效 key，其他 key 继续服务。实际插件实现需实施时继续检查，不直接移入插件框架。

本地已有预算、并发槽续租、冷却和粘连；应扩展作用域和状态版本，沿用 ADR-021。与 10 月 U03（分组限流）、U04（旧成功清新冷却）、F02（额度窗口调度）合并成同一验收包，不叠第二个限流器。当前中转没有公开额度窗口时标记未知，不伪造 5h/7d 数据；可先基于本渠道响应和配置预算调度。

## 六、推荐实施批次与验收

| 批次 | 交付内容 | 可检验的完成标准 |
| --- | --- | --- |
| A：线上故障及身份基础 | 修复定价统一保存/按钮可达；验证 codex-auto-review 原生 Responses 精确路由；显示生效身份与来源；身份作用域和 legacy 迁移 | 1600×900、窄屏及缩放均可保存并重载；custom namespace 不再因错误归一化本地拒绝；A/B 租户、两个会话不串，重启稳定，failover 不残留 |
| B：主要账号保护能力 | 统一 HTTP/WS/compact/维护身份解析；错误归因；额度窗口/冷却版本；有界并发与退避；严格出口快照 | mock 401/403/429/代理失败得到正确范围和次数；旧成功不清新冷却；乱序帧/写库失败/重启不复活旧冷却；维护与推理出口策略一致 |
| C：原生 OAuth 接入前置 | 单一刷新所有者、CAS/receipt、终止错误隔离、升级期间刷新者切换 | 多进程同凭据只发生一次交换；发送前失败可恢复；发送后丢响应/崩溃不重放；客户端取消仍保存新 token；SQLite/MySQL/PostgreSQL 均验证 |
| D：按渠道试点 TLS 兼容 | 经过验证的 TLS profile、端点能力和版本管理、池隔离与回滚 | 本地抓包比对 ClientHello/ALPN；HTTP/HTTPS/SOCKS 代理正确；代理错误零直连；证书错误拒绝；长流/WS 不退化；当前中转小范围有可测收益才推广 |

默认先按 A→B→C→D；如果接下来要直接导入 OAuth 账号，C 提前到 B 同批并成为启用门槛。计费明细、更多模型接入和非必要 UI 优化顺延，已有安全依赖更新另行保留，不因本专项删除。

后台建议增加“客户端与账号保护”诊断页：实际生效 profile、作用域、版本来源、代理来源、凭据状态、冷却理由/恢复时间和最近状态变化。敏感标识仅展示摘要；不显示无统计依据的“防封成功率”或单一风险分数。预览应复用实际解析器并通过无网络、脱敏的 dry-run 输出，便于审核迁移。

生产试点比较相同渠道、负载和模型下的 401/403/429 分类、成功率、重复刷新数、单位成功请求的上游尝试数、冷却准确度与长流完成率。此类指标证明稳定性变化，不能单凭前后相关性归因于厂商风控机制。

## 本轮已验证与未验证

- 已读取固定源码和关键测试，并记录 21 个路径的近期提交历史；增加官方客户端基准。
- Playwright/SSH 只读结果相互印证全局身份配置；只输出开关和汇总数量。
- `go run .../identity_repro.go` 调用本地真实函数，三个跨调用者标识合并断言和一个非法代理拒绝断言均为 true。
- `go test ./service ./relay/channel -run '^(TestApplyClientIdentity|TestApplyDefaultUpstreamUserAgent|TestApplyManagedUpstreamUserAgent|TestShouldForceCodexIdentity|TestHTTPTransportFingerprintDoesNotExposeProxyCredentials|TestHTTPClientCacheKeyIncludesProxyAndTLS)' -count=1` 通过。这些验证现状，不是新功能修复验收。
- 未对生产发起推理、刷新、遥测或配置写入；未运行六个参考项目的完整测试、未实测其风控效果。TLS 和 OAuth 竞态中标记为源码发现的项目不冒充线上复现。
- codex2api 固定快照未见根目录 LICENSE，GitHub 元数据未给出许可证；独立实现其可观察行为，直接复制代码前单独核对许可。其他来源也按实际文件许可适配并保留必要说明。

[s-account]: https://github.com/Wei-Shaw/sub2api/blob/530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484/backend/internal/service/openai_codex_account_identity.go
[s-fp]: https://github.com/Wei-Shaw/sub2api/blob/530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484/backend/internal/service/openai_codex_fingerprint.go
[s-identity]: https://github.com/Wei-Shaw/sub2api/blob/530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484/backend/internal/service/openai_codex_identity.go
[s-tls]: https://github.com/Wei-Shaw/sub2api/blob/530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484/backend/internal/pkg/tlsfingerprint/dialer.go
[s-refresh]: https://github.com/Wei-Shaw/sub2api/blob/530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484/backend/internal/service/oauth_refresh_api.go
[s-refresh-service]: https://github.com/Wei-Shaw/sub2api/blob/530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484/backend/internal/service/token_refresh_service.go
[s-transport]: https://github.com/Wei-Shaw/sub2api/blob/530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484/backend/internal/service/openai_upstream_transport_error.go
[p-profile]: https://github.com/router-for-me/CLIProxyAPI/blob/e96d4c4818851cf0e96d27b2bc8b67379e449d9e/internal/config/claude_fingerprint_profile.go
[p-seed]: https://github.com/router-for-me/CLIProxyAPI/blob/e96d4c4818851cf0e96d27b2bc8b67379e449d9e/internal/runtime/executor/helps/claude_cli_identity_seed.go
[p-journal]: https://github.com/router-for-me/CLIProxyAPI/blob/e96d4c4818851cf0e96d27b2bc8b67379e449d9e/internal/auth/codex/refresh_journal.go
[p-patch]: https://github.com/router-for-me/CLIProxyAPI/blob/e96d4c4818851cf0e96d27b2bc8b67379e449d9e/internal/auth/codex/refresh_patch.go
[p-outcome]: https://github.com/router-for-me/CLIProxyAPI/blob/e96d4c4818851cf0e96d27b2bc8b67379e449d9e/internal/auth/codex/refresh_outcome_test.go
[p-tls]: https://github.com/router-for-me/CLIProxyAPI/blob/e96d4c4818851cf0e96d27b2bc8b67379e449d9e/internal/runtime/executor/helps/utls_client.go
[p-tos]: https://github.com/router-for-me/CLIProxyAPI/blob/e96d4c4818851cf0e96d27b2bc8b67379e449d9e/internal/runtime/executor/antigravity_executor_credits.go
[c-identity]: https://github.com/james-6-23/codex2api/blob/41e8d86bf0d4eedee5322e7668af595df4142658/proxy/codex_client_identity.go
[c-modes]: https://github.com/james-6-23/codex2api/blob/41e8d86bf0d4eedee5322e7668af595df4142658/auth/codex_fingerprint_mode.go
[c-claude]: https://github.com/james-6-23/codex2api/blob/41e8d86bf0d4eedee5322e7668af595df4142658/auth/claude_fingerprint.go
[c-claude-test]: https://github.com/james-6-23/codex2api/blob/41e8d86bf0d4eedee5322e7668af595df4142658/proxy/claude_identity_boundary_test.go
[c-tls]: https://github.com/james-6-23/codex2api/blob/41e8d86bf0d4eedee5322e7668af595df4142658/proxy/utls_transport.go
[c-auth-tls]: https://github.com/james-6-23/codex2api/blob/41e8d86bf0d4eedee5322e7668af595df4142658/auth/utls_client.go
[c-egress]: https://github.com/james-6-23/codex2api/blob/41e8d86bf0d4eedee5322e7668af595df4142658/proxy/account_bound_egress.go
[c-telemetry]: https://github.com/james-6-23/codex2api/blob/41e8d86bf0d4eedee5322e7668af595df4142658/proxy/codex_telemetry_transport.go
[c-lease]: https://github.com/james-6-23/codex2api/blob/41e8d86bf0d4eedee5322e7668af595df4142658/auth/oauth_refresh_lock.go
[c-cooldown]: https://github.com/james-6-23/codex2api/blob/41e8d86bf0d4eedee5322e7668af595df4142658/auth/usage_cooldown_persist.go
[c-fifo]: https://github.com/james-6-23/codex2api/blob/41e8d86bf0d4eedee5322e7668af595df4142658/auth/usage_frame_observation_test.go
[n-refresh]: https://github.com/QuantumNous/new-api/blob/b388140acfd80369ec6243b16869daff1b4051f4/service/codex_credential_refresh.go
[n-status]: https://github.com/QuantumNous/new-api/blob/b388140acfd80369ec6243b16869daff1b4051f4/model/channel_status_test.go
[n-plugin-test]: https://github.com/QuantumNous/new-api/blob/b388140acfd80369ec6243b16869daff1b4051f4/service/provider_plugin_test.go
[l-common]: https://github.com/BerriAI/litellm/blob/e38187324ad3b49e8111c6823a8cfb3c7d6c2f7d/litellm/llms/chatgpt/common_utils.py
[l-cooldown]: https://github.com/BerriAI/litellm/blob/e38187324ad3b49e8111c6823a8cfb3c7d6c2f7d/litellm/router_utils/cooldown_handlers.py
[l-beta]: https://github.com/BerriAI/litellm/blob/e38187324ad3b49e8111c6823a8cfb3c7d6c2f7d/litellm/anthropic_beta_headers_manager.py
[o-client]: https://github.com/openai/codex/blob/b7056f3408522716b6643c8b3e23ce7c9b53b041/codex-rs/login/src/auth/default_client.rs
[o-headers]: https://github.com/openai/codex/blob/b7056f3408522716b6643c8b3e23ce7c9b53b041/codex-rs/codex-api/src/requests/headers.rs
[o-ws]: https://github.com/openai/codex/blob/b7056f3408522716b6643c8b3e23ce7c9b53b041/codex-rs/codex-api/src/endpoint/responses_websocket/connector.rs
