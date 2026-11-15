# 模型定价、Responses 工具兼容、渠道身份与托管恢复

Status: validated_source
Created: 2026-11-15
Last updated: 2026-11-15

## Goal

按确认方案修复定价保存和 namespace 工具故障，提供每渠道独立身份及客户端可透明续接的受管上下文，完成源码整体逻辑与效率检查。用户授权源码修改和验证，没有要求本次发布部署。

## Result

工作树 `D:/Code/renewapi/repair-20261017`，main，基于 `b7788ff39176337f02b5067ca96a85afa2715584`。源码未提交/推送/发布/部署，未触碰其他 worktree 或生产配置。

- P0：统一定价快照、保存/导出/改名/模式切换，合法工具能力分类和原生 Responses 回退；旧 scrub 不再删除未知父链冒充恢复。
- P1：每渠道加密身份、稳定凭据 slot、HTTP/WS/header/body 共用 profile、严格出口、管理预览/修改/CAS。
- P2/P3：owner 隔离加密不可变响应图、逻辑 ID、原生 store 首试增量/重试完整重放、HTTP/SSE/WS 共用恢复、已知 compaction preimage、工具结果保留。
- P4：有界整轮缓冲、终态内容/身份校验、已有账本结算意图与 receipt 协调、重启恢复和交付阶段；shadow 保持被动。
- P5 已实现部分：共享 OAuth grant journal/receipt/锁后重读/CAS、账户 FIFO/租约、账户/模型/代理冷却、通用额度 header 与迟到成功保护。
- P5/特殊能力保留边界：未自动目标重压缩、私有 Files/container/未知 item_reference 迁移、厂商 WHAM quota 全覆盖或 TLS 指纹试点。realtime 已暴露的未完成输出不能撤回重放。这些不作为已交付能力。

## Validation

全仓 Go 测试、vet、build；受影响路径 race；前端 234 项测试、类型、改动文件 lint、构建均通过。SQLite、MySQL 5.7/8.4、PostgreSQL 9.6/16 状态/身份/刷新/CAS/FIFO/租约通过；30 组独立进程崩溃恢复覆盖钱包/订阅和三种账本模式。Playwright 真保存、HTTP 500 草稿保留、双击单写、改名碰撞、四视口、身份及恢复/出口/队列设置读回通过。

完整实现、验证命令入口、默认资源上限、keyring/灰度/回退说明和能力边界见[实施验收](../../docs/audits/2026-11-managed-recovery-implementation.md)。生产 v1.5.1 和 37 个自定义渠道仍是调查基线，不是新功能的生产验收。

## Review decisions

主数据库为事实源；不可变分支代替可变共享 head。身份、历史、刷新凭据使用独立 keyring；不能在日志中展开状态 SQL。存储错误不当 miss；已准备资金意图不重新调用模型；上游未知副作用不自动重放。默认 UA 版本不变，不把身份隔离描述为防封禁保证。

后续若部署，先配独立密钥和备份、铺可读新表/ID 的版本，再逐渠道灰度。旧 ID 在关闭新写入后仍要读至到期，长期身份记录仍需旧密钥。此次没有部署动作。

## Sources

- [源码方案](../../docs/audits/2026-11-transparent-failover-source-plan.md)
- [ADR-023](../../docs/decisions/023-managed-conversation-recovery.md)
- [初始故障与更新调查](../../docs/audits/2026-11-pricing-tools-upstream-plan.md)
- [账号保护源码专项](../../docs/audits/2026-11-client-identity-account-protection-review.md)
- [渠道身份与会话设计](../../docs/audits/2026-11-channel-identity-session-design.md)
- [上游取舍](../../UPSTREAM_PORTS.md)

调查证据位于 `D:/Code/renewapi/audit/20261115-client-risk-source-review/`；最终日志在 `D:/Code/renewapi/audit/20261115-final-*.log`；数据库与浏览器证据路径见实施验收。没有执行参考项目的全套测试，没有推进完整上游历史审计基线。
