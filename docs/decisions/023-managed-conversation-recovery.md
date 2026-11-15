# ADR-023：网关托管上下文与客户端无感渠道切换

状态：接受并在本地源码实施，2026-11-15；验证通过，尚未发布部署。实际能力以[实施验收](../audits/2026-11-managed-recovery-implementation.md)为准。

## 背景

每渠道独立设备/安装 ID 与客户端续接同一对话必须同时成立。全局身份覆盖、有限连接内 WS 历史和独立 encrypted scrub 不能满足这一目标；只删除 previous_response_id 或密文不等于恢复父历史。

## 决策

1. 身份按渠道持久化；多凭据用稳定 slot，正常 OAuth 轮换保持主体，重排 key 不换身份。session/thread 再按用户、令牌和逻辑会话投影。真实账号共享的刷新与额度协调不因渠道空间不同而取消。
2. 主数据库保存 owner 隔离的加密 checkpoint、资源索引和提交 receipt；独立 AES-GCM keyring 与数据库分开配置。AAD 绑定归属、父链、目标、状态能力；数据库错误不能伪装为未命中。
3. 使用不可变响应图，每次续接产生自己的子节点，替代方案阶段共享可变 head 的 CAS。原生响应 ID 与逻辑 ID 分开；合法并行分支不会互相覆盖。配置版本和身份 revision 改变会使旧原生绑定失效。
4. Responses HTTP/SSE/WS 共用恢复服务。先恢复完整已知历史，再应用渠道授权、目标能力和统一出站 profile。原生 store:true 且绑定未改变的首试可用增量，后续尝试从不可变完整快照重新投影。
5. 普通消息与函数/custom 工具历史在可等价表达时迁移，call ID、工具结果与客户端协议保持。未知父历史、私有资源、隐藏签名和可能已产生未知副作用的服务端工具不能靠删字段迁移。已知 compaction 展开完整 preimage，自动目标重压缩暂未实现。
6. realtime 在首语义前允许换候选，实际内容已交付后不混流。atomic_turn 用有界缓冲，终态校验、持久准备与结算通过后交付；它增加首输出延迟。未完成的已暴露流不作为成功 checkpoint。
7. 资金仍由现有 BillingSession/ledger 决定。checkpoint/receipt 与结算意图同一事务，账本 reconciliation 完成资金动作，checkpoint 随后发布；准备后不再生成模型回答。无法保证无幂等键的客户端重新发起请求 exactly-once。
8. shadow 在正常计费成功后尽力保存，不改响应或降低请求可用性；managed 明确要求存储成功。上游 store 与本地托管分离，管理界面提示 store:false 仍可能本地保存。要求不落盘使用 off。
9. OAuth 按真实 grant 共享所有权，sent 后结果未知不重放；成功 receipt 单独有界持久化，锁后重读并以 key/config version CAS 应用结果。账户 FIFO/租约、模型 quota 与代理冷却按渠道选择开启，迟到成功不能清掉较新故障。
10. 功能分开灰度。关闭新请求 admission 后，已有托管 ID 仍必须可读至到期；不兼容旧镜像不能直接回退。长期身份记录也依赖旧密钥，不能只按会话保留期删除 keyring 成员。

## 与既有 ADR 的关系

- ADR-017 对未知 previous_response、外国签名和工具语义的保护保留；只有网关已验证完整重建时才进入新的恢复路径。
- ADR-021 首语义前缓冲和提交后不混流保留；增加整轮缓冲、真实客户端暴露与资金提交的区分。
- ADR-022 协议不可表达与 malformed 分类推广到 Responses 工具和原生 fallback。
- 各参考项目仅采用适合本地架构的行为，不合并独立历史、不复制未经授权源码、不生成伪造遥测。

原始设计见[源码方案](../audits/2026-11-transparent-failover-source-plan.md)。验收包括四数据库版本、HTTP/WS 工具续接、30 组进程崩溃重启及浏览器真实配置保存；不代表生产上游一定支持全部工具或不触发风控。
