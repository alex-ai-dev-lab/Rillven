# 2026-11-15 模型定价、Responses 工具兼容及上游更新方案

状态：调查完成，实施计划待执行。按用户“先给整体方案”的要求，本轮没有修改产品代码、线上价格、路由或部署。

最新实施入口：[客户端无感切换与原始 bug 的完整源码方案](2026-11-transparent-failover-source-plan.md)。用户明确要求网关内部完成恢复，因此改用托管上下文、统一恢复协调和可选整轮缓冲；逐文件修改及 P0–P5 顺序以新文为准，下文保留初次调查证据。

范围更新：用户随后要求深入其他项目源码，优先防风控、防封禁和客户端伪装。已完成[客户端身份与账号保护专项复核](2026-11-client-identity-account-protection-review.md)，增加完整源码快照、关键实现/测试、21 个路径的提交历史和官方 Codex 客户端基准；下方实施顺序已按新重点调整。

用户进一步明确设备/安装 ID 按渠道独立；[会话细化方案](2026-11-channel-identity-session-design.md)补充内部逻辑会话、按渠道投影、稳定粘连和状态资源强绑定，并将现有 encrypted scrub 兼容路径的历史完整性检查纳入 A/B 批次。

## 基线与证据范围

- 线上：`renewapi:1.5.1` / `new-api-v1-5-1`，镜像修订 `b268e67984e0707cfde3d260ba6724c93a03eee7`，健康。
- 开发：`D:/Code/renewapi/repair-20261017`，`main@b7788ff39176337f02b5067ca96a85afa2715584`，与远端 main 一致；调查开始时工作树干净。
- `source`、`main-integration` 是旧工作树，含其他未提交修改，本次没有使用或覆盖。
- 通过用户指定的 `Token/192.env` SSH 只读检查容器及 SQLite 错误日志；通过已有浏览器登录状态读取设置和复现布局。凭据不进入文档。
- 检查五仓各最新 100 条提交、最新 release 信息，并补充 2026-10-17 以来 namespace/custom/tool 关键词定向检索；共保存 47 份候选提交详情。重点工具候选已用 compare 确认包含在所固定的 main HEAD 中。
- 本轮是候选选型和定向代码比较，不是五仓所有新增提交的完整审计，不推进 `UPSTREAM.md` 的完整历史基线。HEAD 候选不自动代表已经包含于最新 release。
- 本地原始证据：`D:/Code/renewapi/audit/20261115-pricing-tools-upstream/`，包括 GitHub 元数据、patches、祖先关系、线上日志摘要及布局截图。

| 上游 | 查询时最新 Release | 固定 main HEAD |
| --- | --- | --- |
| [NewAPI](https://github.com/QuantumNous/new-api) | v1.0.0-rc.51 | `b388140acfd80369ec6243b16869daff1b4051f4` |
| [Sub2API](https://github.com/Wei-Shaw/sub2api) | v0.2.21 | `530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484` |
| [CPA](https://github.com/router-for-me/CLIProxyAPI) | v8.0.72 | `e96d4c4818851cf0e96d27b2bc8b67379e449d9e` |
| [LiteLLM](https://github.com/BerriAI/litellm) | v1.109.1 | `e38187324ad3b49e8111c6823a8cfb3c7d6c2f7d` |
| [codex2api](https://github.com/james-6-23/codex2api)（用户已确认） | v3.1.6 | `41e8d86bf0d4eedee5322e7668af595df4142658` |

## 已确认的两个问题

### A. 定价编辑区按钮被裁切，保存存在两层状态

线上浏览器 1600×900 下，“更新”按钮 y≈1126，外层卡片底部 y≈847；卡片 `clientHeight=768`、`scrollHeight=1186` 且 `overflow-y:hidden`。外层主滚动区没有获得这部分内容高度，所以编辑区内部滚动不能解决。截图见 `pricing-clipped-1600x900.png`。

相关代码：

- `web/default/src/features/system-settings/models/model-ratio-visual-editor.tsx:989`：侧栏采用 `h-[calc(100vh-8rem)] min-h-[620px]`，未扣除同步区、页签和工具栏占用的高度。
- `model-pricing-sheet.tsx:958`：本地表单底部“更新/添加”按钮。
- `model-ratio-visual-editor.tsx:839`：更新仅写入父层草稿，并提示再点“保存模型价格”。
- `model-ratio-form.tsx:121`：顶部按钮直接提交父表单，未先提交当前编辑器。
- `ratio-settings-card.tsx:565`：后端已有批量配置保存通道，可以复用。

因此截图顶部虽然有“保存模型价格”，当前编辑区修改仍可能尚未进入待保存数据。这是实际可用性及数据状态缺陷。

计划：

1. 修正页面高度与滚动职责；编辑表单主体滚动，操作栏始终可达。收紧顶部空白，覆盖窄屏和缩放。
2. 顶部“保存模型价格”统一执行：校验当前编辑器 → 合并草稿 → 批量保存 → 成功后更新已保存快照。右侧按钮明确标为“应用到草稿”，或复用统一保存入口，消除含糊的“更新”。
3. 展示未保存状态与内联价格差异；请求失败保留输入，切换模型/JSON 模式不静默丢失修改。无需照搬上游每次保存弹出确认框的交互。
4. 保留现有批量保存和计费模式，零价与未设置分开，避免 UI 顺带改变计费行为。

参考 [NewAPI 72f3dc7daa](https://github.com/QuantumNous/new-api/commit/72f3dc7daa) 的 `commitOpenEditor`、dirty 状态和差异预览；[e3e74205fe](https://github.com/QuantumNous/new-api/commit/e3e74205fe) 的定价错误引导。其新版定价/插件模块结构不同，按行为适配到本地 UI。

### B. codex-auto-review 被强制转为 Chat，工具预检本地拒绝

证据链：

1. 线上 `ModelEndpointDefaults` 启用了 `normalize_text_endpoints=true`。
2. `codex` 前缀规则指定 `default_endpoint=openai`、`fallback_endpoint=openai`（Chat Completions），允许自动校正；没有 `codex-auto-review` 精确规则。渠道 267 的模型端点绑定列表为空。
3. `relay/responses_handler.go:53` 进入模型默认端点重写；`:70` 在校验失败时返回 `400 invalid_request + SkipRetry`。
4. `service/openaicompat/responses_bridge_tools.go:188` 只允许 namespace 子项为 `type=function`，否则返回用户看到的原文。
5. 最近三天错误日志中命中 3 条：185846、185851、185856，模型均为 `codex-auto-review`、渠道均为 267、单次尝试 0–1 ms，原因为 `Responses namespace "functions" contains a non-function tool`。

该错误发生在 RenewAPI 发出上游请求之前，与该模型零价设置无直接关系。历史错误日志没有保存被拒绝子项的完整结构；`custom`/自由文本工具是重点回归对象，但不能仅据错误字符串断言那三次一定是哪一种工具。

计划分两步：

1. **先恢复适合该模型的原生路径**：在隔离环境验证渠道 267 的该别名能够原生处理 Responses，再为 `codex-auto-review` 采用精确 Responses 端点规则。保留其他模型、显式绑定和现有路由优先级；记录配置前后值以便回滚。
2. **再补齐通用兼容**：在现有桥接层支持 namespace 内 function/custom、顶层及嵌套 custom 声明、`tool_choice`/`allowed_tools`、`additional_tools`、deferred 身份以及请求/历史/流式/非流式输出的双向还原。字符串输入、call_id、namespace 和工具类型均须保真；重名、长名和 `__` 碰撞按确定规则处理。

原生 Responses 保留原始工具语义。跨协议遇到无法无损表达的 grammar、内置工具或加密内容，采用类型化“当前路径协议不兼容”，在允许的范围内选择兼容候选；结构错误仍返回 400。复用 ADR-022 的错误分类思路，保留显式绑定、Codex 身份、首语义输出和健康计数边界。仅把 `custom` 改成 `function` 或删除工具不足以修复往返调用。

直接参考：

- [CPA 2443e8830c](https://github.com/router-for-me/CLIProxyAPI/commit/2443e8830c)：Chat 嵌套 custom 声明、grammar 布局、tool_choice 与响应侧元数据归一化；这是相邻能力，不是可直接 cherry-pick 的 RenewAPI 修复。
- [LiteLLM 8207a57d84](https://github.com/BerriAI/litellm/commit/8207a57d84)：已加载 deferred 顶层工具保留 `namespace == name` 身份；[9fb29c7e38](https://github.com/BerriAI/litellm/commit/9fb29c7e38)：flat `allowed_tools`。
- [codex2api f21adb5a55](https://github.com/james-6-23/codex2api/commit/f21adb5a55)：namespace/custom/tool_search 与历史项的可逆映射；[f8dd147e79](https://github.com/james-6-23/codex2api/commit/f8dd147e79)：tool_choice 回显与双下划线还原。

## 上游更新适配队列

下表均是计划，不代表已移植。CONFIRMED GAP 指代码或浏览器已确认缺口；VERIFY 指需实施前先证明 RenewAPI 存在同类问题。

| 优先级 | 来源与候选 | 本地判断及实施范围 |
| --- | --- | --- |
| P0 | NewAPI 定价保存；CPA / LiteLLM / codex2api 工具兼容 | CONFIRMED GAP；按前两节实施，先恢复线上可用性。 |
| P1 | NewAPI [9823ebc7ea](https://github.com/QuantumNous/new-api/commit/9823ebc7ea)、[651d5a213f](https://github.com/QuantumNous/new-api/commit/651d5a213f) | CONFIRMED GAP；本地 `InputTokenDetails` 没有 `cache_creation_input_tokens` 别名，xAI SSE 只复制总量而遗漏明细。补字段及跨协议用量归一化；多个等价缓存字段取最大有效值，不能相加重复计费。 |
| P1 | Sub2API [fe093d76bd](https://github.com/Wei-Shaw/sub2api/commit/fe093d76bd)，NewAPI 12h cache-write 方向 | VERIFY；缓存写入按 TTL 细分需贯通 usage、价格、账本和日志，单独计费里程碑，兼容旧数据；不直接覆盖本地账本。 |
| P1 | NewAPI [ada15c050e](https://github.com/QuantumNous/new-api/commit/ada15c050e)、[433830b4e2](https://github.com/QuantumNous/new-api/commit/433830b4e2)；codex2api [8d94c7872f](https://github.com/james-6-23/codex2api/commit/8d94c7872f)、[df3a72f712](https://github.com/james-6-23/codex2api/commit/df3a72f712) | 手工禁用保护在本地 `UpdateChannelStatus` 缺失；其余 VERIFY。统一处理管理员操作与晚到请求竞态、过期额度快照、冷却持久化失败重试。复用本地 channel/key 状态模型，不引入另一套账号池。 |
| P1 | Sub2API [e401c58053](https://github.com/Wei-Shaw/sub2api/commit/e401c58053)、[0578fb160e](https://github.com/Wei-Shaw/sub2api/commit/0578fb160e)；CPA [cf07c78abd](https://github.com/router-for-me/CLIProxyAPI/commit/cf07c78abd)、[f2efe2c413](https://github.com/router-for-me/CLIProxyAPI/commit/f2efe2c413) | VERIFY；扩展 `codex.rate_limits`/metadata 非语义事件、签名-only/零输出结束判断、WS 后续轮失败不重放第一轮。已有 staging、首输出超时、终态检查和关闭逻辑继续复用。 |
| P1 | NewAPI [706144769e](https://github.com/QuantumNous/new-api/commit/706144769e)、[77436271f4](https://github.com/QuantumNous/new-api/commit/77436271f4)；codex2api [1cff410b5d](https://github.com/james-6-23/codex2api/commit/1cff410b5d) | Redis 元数据完整性先比较本地鉴权/原子额度机制；Go 和前端依赖单独升级扫描。本地 Go 1.25.1、DOMPurify 存在旧依赖及 override，按新扫描和受支持版本统一锁定，不机械采用旧报告的版本号。 |
| P1/P2 | Sub2API [b2a695d617](https://github.com/Wei-Shaw/sub2api/commit/b2a695d617)、[f2b0a32b1f](https://github.com/Wei-Shaw/sub2api/commit/f2b0a32b1f)、[85f43b640a](https://github.com/Wei-Shaw/sub2api/commit/85f43b640a) | VERIFY；Codex Desktop page context、sealed function 元数据、工具循环会话稳定性。只处理可识别的特殊输入，保留外部文本的低信任语义，不能把缺 call_id 的任意工具输出伪造成合法历史。 |
| P2 | LiteLLM [bc3113fff2](https://github.com/BerriAI/litellm/commit/bc3113fff2)、[5849906ddb](https://github.com/BerriAI/litellm/commit/5849906ddb) | 结合旧 U12：Gemini/Vertex 用 `parametersJsonSchema` 表达联合 schema；计数请求与推理实际参数一致，且尊重端点各自支持的 schema 字段。不能用删除 oneOf/const 来假装兼容。 |
| P2 | CPA [91aca317b0](https://github.com/router-for-me/CLIProxyAPI/commit/91aca317b0)、[c3a6c7416c](https://github.com/router-for-me/CLIProxyAPI/commit/c3a6c7416c)；NewAPI [0409deccff](https://github.com/QuantumNous/new-api/commit/0409deccff) | VERIFY；Gemini thought signature 往返、typed thinking 数组、DeepSeek developer role，分别进入供应商适配器，避免全局改写。 |
| P2 | codex2api [1856d54671](https://github.com/james-6-23/codex2api/commit/1856d54671)、[63865f1bbe](https://github.com/james-6-23/codex2api/commit/63865f1bbe) | 功能候选：按 API Key 授权的上游额度/重置时间展示、限额期间暂停质量探测。先定义租户与共享额度边界；质量检测只作辅助信号，不替代真实服务模型证据。 |

## 去重、延后与边界

- 继续追踪 10 月报告中未实施的 U01 Playground 账号隔离、U02 Claude 零值/缓存-only、U03 分组限流作用域、U04 晚到成功清除新冷却、U05 reasoning 与正文同块、U08 多行 beta、U12 schema。与本轮同类候选合并验收，不能将旧 DEFERRED 条目记为已发布。
- 原生 Codex OAuth、会话粘连、Responses WS、compaction、批量保存、有限故障切换、长流 H2 ping 已存在，本轮只补缺口。
- NewAPI [30748c6558](https://github.com/QuantumNous/new-api/commit/30748c6558) 的访问日志查询凭据保护：本地 logger 已仅记录 `EscapedPath()`，此路径 ALREADY_PRESENT；不为跟随上游重新引入查询字符串日志。
- LiteLLM 的递归 fallback 修复、流关闭顺序、数据库中断保留聚合记录作为行为对照；本地已有六渠道上限及生命周期/账本机制，没有证据支持替换整套实现。
- NewAPI 插件市场和 MoeForge/moego、CPA 多租户 WS tunnel/插件调度、LiteLLM Python/Rust gateway/SQLx 数据层、codex2api 账号池和整套前端暂不纳入。其架构跨度明显大于本轮问题收益；CPA “默认关闭连接复用”等供应商策略也不全局复制。
- 最新模型与价格变化通过现有官方目录/价格同步及供应商适配器处理，保留人工定价；不把上游静态价格表直接覆盖到生产。
- codex2api 的仓库元数据未给出许可证，查询根目录也未发现 LICENSE 文件。本计划参考公开协议行为并独立实现；代码级复制需另行确认许可范围。LiteLLM 候选限公开对应模块，不包含企业模块。

## 实施顺序与验收

1. **A / 线上故障与身份基础**：定价布局与统一保存；验证并纠正 `codex-auto-review` 原生 Responses 路由；生效身份预览、凭据/租户/会话隔离及 legacy 迁移。通用工具桥接若尚未满足保真要求，独立进入 B，不能阻塞原生路径修复。
2. **B / 客户端兼容与账号保护**：统一推理/WS/compact/维护身份解析，工具保真、错误归因、严格代理出口、额度窗口/冷却状态版本和有界并发退避；合并旧 U03/U04/F02 验收。当前线上 37 条启用渠道均为自定义地址且无原生 Codex OAuth，优先覆盖这些真实路径。
3. **C / 原生 OAuth 接入前置**：跨实例刷新所有权、CAS/结果记录、结果未知与终止错误隔离；新增原生账号前必须完成。如果提前接入原生 OAuth，C 提前与 B 同批。
4. **D / 可选 TLS 兼容试点**：按渠道验证 TLS profile、HTTPS/SOCKS 代理、连接池隔离、长流和 WS；确认收益再推广。客户端伪装不默认合并全站会话、不全局替换成 Chrome TLS。

计费明细、更多模型和非必要 UI 功能顺延；依赖安全、Redis 鉴权完整性仍保留在后续维护队列，不因本次专项取消。具体源码取舍、分批验收和验证边界以[专项方案](2026-11-client-identity-account-protection-review.md)为准。

验收要求：

- 浏览器：1440×900、1920×1080、手机、125%/150% 缩放；编辑后直接顶端保存、切换模型/模式、零价/未设置、模拟保存失败；刷新页面和 GET 读回均一致。测试价格写入在隔离环境进行。
- 协议：function/custom 混合 namespace、嵌套 custom、grammar、重名/长名、deferred/allowed_tools/additional_tools、历史 call_id、HTTP/SSE/WS、工具输入增量和终态还原；不可表达与 malformed 分开验证；已输出内容不跨渠道重放。
- 计费：普通输入、缓存读取/写入和别名并存、显式零值、失败退款、重复终态、长流中断；计费模式保持现状，SQLite/MySQL/PostgreSQL 验证按模块风险执行。
- 发布：完整仓库门禁；从 Releases 获得对应 SHA 镜像；隔离副本验证和备份；候选端口验证后 Caddy 切换、旧连接排空；保留镜像与配置回滚。版本名称为提议，实施时按最终范围确定。

本轮实际验证：线上布局复现及 SSH/管理 GET 只读核对完成；运行现有 `TestResponsesToClaudePreservesNamespaceTools`、`TestShouldUseModelDefaultTextEndpointForResponses`，两个包通过。另以审计目录的 `namespace_repro.go` 在本地执行同一预检函数：function 在原生/Chat 路径均通过；namespace 内 custom 在原生 Responses 预检通过，Chat 预检精确返回本次报错。合成复现未发送推理请求，不证明上游最终支持或那三次请求的具体工具类型。这些验证不代表修复、完整测试或新版本已完成。
