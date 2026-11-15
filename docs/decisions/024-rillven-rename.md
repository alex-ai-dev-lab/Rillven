# ADR-024：Rillven 命名与升级兼容

Status: accepted
Date: 2026-11-15

## Context

用户要求项目改名为 Rillven，GitHub 仓库改为 `alex-ai-dev-lab/rillven`，
检查后仅在 main 提交。旧部署、密文、会话及已发布版本必须继续可用。
本决策替代 ADR-001、003、010、013 中未来产物的 RenewAPI 命名约定，
其发布门禁、版本身份、不可变标签/附件与历史清理约束继续适用。

## Decision

- 产品界面、默认站点名、CLI 和当前文档使用 Rillven，仓库 URL 使用新地址。
  只将空名称和旧产品默认值 `New API` / `NewAPI` / `RenewAPI` 映射到新默认值；
  自定义名称保留。映射作用于配置读取、API 返回与浏览器缓存，不批量重写数据库。
- 新构建镜像为 `rillven:<tag>`，产品标签为 `rillven-v*`，源码包标签为
  `rillven-build-*` / `rillven-source-*`。不更改 VERSION，不重建历史标签/附件，
  旧链接由 GitHub 重定向，下载旧版本仍须使用其原始名称和配套配置。
- 容器主程序为 `/app/rillven`，保留 `/app/new-api` 符号链接。Compose 服务名
  `new-api`、项目/容器默认名、网络名、Unix 账户和数据挂载路径不变。
  已有部署不要因为改名移动目录或更换 `COMPOSE_PROJECT_NAME`。
- 新部署参数使用 `RILLVEN_IMAGE/HOST_PORT/DATA_DIR/LOG_DIR/PUBLIC_DIR`，
  非空值优先，旧 `NEWAPI_*` 继续回退。部署/回滚助手对一次操作同时覆盖两种
  IMAGE 变量，以适配新旧 Compose 模板并避免 `.env` 中的优先值阻止回滚。
- 状态密钥使用 `RILLVEN_STATE_KEYS` 与 `RILLVEN_STATE_ACTIVE_KEY`；任一非空
  就选择整个新配置组，不完整时拒绝使用，也不回退到已缓存的密钥。
  两者均空才选择 `RENEWAPI_*` 旧组。改变量名时复制同一组密钥，不重新生成。
  账户队列的三个 `RILLVEN_UPSTREAM_*` 参数逐项兼容旧 `RENEWAPI_*`，
  Compose 同时传递两种前缀，由应用做优先选择。
- 保留 `resp_renew_`、加密 AAD、游标域、缓存键派生、会话/cookie/localStorage
  名称和外观迁移版本；它们是数据或协议契约，不是产品展示。
- Go module/import 路径 `github.com/QuantumNous/new-api` 保持稳定，便于现有
  内部依赖与构建 ldflags 兼容。项目源码归属以仓库、NOTICE 和文档说明为准。
- 保留 New API / SnowAPI 等原作者署名、许可证和显著的上游链接。
  历史任务、审计与发布记录保留事件发生时的品牌、路径、标签和验证证据。

## Validation and rollout

验证状态密钥别名、跨前缀读取与轮换、不完整配置拒绝；浏览器验证默认名、
旧缓存、旧默认值、自定义名称、仓库链接及定价/渠道配置真实保存。
验证 Go、前端、发布元数据、Compose 插值及构建脚本；发布 CI 检查新二进制
与旧链接指向同一文件。仓库改名前后核对 repository ID 和默认分支 main。

本次不部署生产，不变更线上配置或既有镜像。推送 main 仅运行已有检查与源码包
发布流程，正式版本标签与服务器升级仍需独立执行。
