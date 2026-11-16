# ADR-025：共享账号身份与凭据事实源

状态：基础源码已实施，真实验收待完成，2026-11-16。

上游账号、安装身份、轮转凭据、渠道路由与下游 owner 分开。新增 per_account 模式，旧 per_channel/legacy/off 保留读取及回退；旧记录 AAD 不原地改写。相同认证主体共享安装身份、额度和准入，成员 session/thread/window 仍独立。渠道不再决定共享账号身份。

主数据库保存账号 UUID、加密种子、凭据与版本、绑定及恢复历史。所有投影发生在实际出站副本并最后校验。父子引用使用同一语义 namespace；资源引用先归属/完整性验证，未知状态不删。刷新按 grant 所有权、数据库时钟、sent/received journal 与 CAS；取消不能丢已收到 rotation，unknown 不重放。

账号 record revision、identity revision 和 profile revision 分开；软件画像升级不重置安装和
owner/thread/window 映射，身份重置必须是明确管理动作。固定官方 Codex
`662a634e88fcc809d77da393d75572c3d814cb27` 的生成算法，UUIDv7 和模型可见数字池分别持久分配。
MySQL 的竞争后读取使用 current/locking read，避免 repeatable-read 快照看不到唯一键 winner。

幂等 claim 以 owner + key + 原始 payload/路径为范围，已发布加密结果可重取；删除/过期和未知结果
不授权重新生成。流式 replay 返回终态完整 output，不伪装成原 delta 序列。严格引用 registry 不把
上游 ID 当 bearer capability；私有资源与未知前像不通过删除字段迁移。

删除先提交不含正文的持久记录再异步清理，保留在途资金/refresh/幂等恢复所需墓碑。旧备份恢复
必须先应用独立保存的最新删除记录，全部在用旧 key 在 CAS 重加密完成前保留。

渠道/分组授权继续表达共享池，不另建并行权限系统。全局/旧渠道不自动迁移；旧 AAD、旧 logical ID
兼容读取。回退需要理解 vault/新状态的兼容版本。配置、API 和限制见 [账号池说明](../ACCOUNT_POOL.md)，
证据见 [验收记录](../account-pool-validation-20261116.md) 和 active task。
Go HTTP/WS 本地出站验证不表示真实账号、供应商额度或官方 TLS/H2 画像已经验证；可选 native
egress、目标自动 compact 和私有资源迁移独立处理。
