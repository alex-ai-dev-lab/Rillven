# 共享账号池源码发布和服务器切换

Status: in_progress
Created: 2026-11-16

用户授权提交 main，用 GitHub Actions 构建离线镜像到 Releases，随后服务器无缝切换。
基线 `98a499224a081ffb3d127c2ca7cb8e90ea5a71f4`，产品版本沿用 v1.6.0；
本次使用精确源码 SHA 的预发行构建，不创建或移动正式产品标签。

本地最终 Go test/vet/build、相关 race、四版本数据库、前端及改动 workflow 检查通过，
详细结果见 `docs/account-pool-validation-20261116.md`，不重复无关检查。

部署先实时核实服务器、现有镜像/端口、Caddy、数据库及 keyring；保留现有资金、密钥和
渠道配置。Release 身份和 SHA256 验证后导入，在线备份的隔离副本先验证迁移和候选请求，
再执行生产加法迁移，预热候选、Caddy reload、旧请求排空，最终确认单 SQLite writer。
保留本次回退材料，不清理历史发布或其他服务。

完整账号池真实 A/B、U/V、quota/OAuth、两个服务进程和官方 TLS/H2 对照仍属后续功能验收；
本次更新镜像不自动为现有生产渠道开启 per_account 或 managed 模式。

证据目录：`D:/Code/renewapi/audit/20261116-account-pool-release-deploy/`，不存凭据或数据库副本。
提交、Actions、Release 和上线证据完成后补充本任务并归档。
