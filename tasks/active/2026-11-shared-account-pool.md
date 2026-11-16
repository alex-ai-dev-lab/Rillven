# 共享订阅账号池

状态：基础源码已实施并通过本地检查，真实验收待完成。2026-11-16。用户授权完整源码改造，要求整体逻辑一致、优雅高效。基线 main `98a4992`，业务 `565ff72`；不改变生产配置。

方案：`D:/Code/outputs/Rillven-shared-account-plan-20261116.md`。需求 1–10 全部保留，不能以局部实现或假上游测试代替实际验收。

## 实施顺序

- P1：账号/身份/凭据/渠道绑定；迁移保留旧 ID；共享刷新、数据库时钟与凭据加密。
- P2：统一实际 HTTP/原生 WS/compact/维护出站，完整关联字段与多用户隔离。
- P3：真实额度窗口、去重选号、准入/公平性、严格历史/引用恢复、幂等交付。
- P4：删除/密钥轮转/备份恢复/日志、管理 UI、迁移回退与观测。
- P5：按证据判断 native egress、自动目标 compact、私有资源恢复；不能静默删未知状态。

## 约束与验证

复用原路由、三库、原生工具、钱包/订阅/账本、checkpoint/receipt。旧 per_channel 模式保留兼容，新增 per_account；账号身份不能依赖调用者或部署。原始请求不可变，输出后不自动换流；未知工具副作用不重放。凭据成功轮转先持久保存，未知发送结果不重用。

测试包括定向、race、全仓 Go/vet/build、前端必要检查、三库/崩溃与 HTTP/原生 WS 两账号两成员矩阵；真实上游、quota、双实例与传输对照单独记录。SQLite settlement/reconcile 错误不能吞掉，作为上线门禁。

## 当前进度

已实现账号/渠道绑定、逐 grant 凭据 vault、取消后的 refresh receipt/CAS、数据库时钟与准入隔离期；新账号随机 UUIDv4 安装 ID，旧安装身份保留。官方 Codex 源码固定到 `662a634e88fcc809d77da393d75572c3d814cb27`，目录 `D:/Code/outputs/rillven-account-pool-official-codex`；线程/turn/context UUIDv7 持久分配，根 session/thread、父子/窗口、User-Agent 平台/终端快照已接入实际出站。

已接通默认 Codex 额度头、原生 WS codex.rate_limits、WHAM 共享探测 lease、multi-key 选号、重复账号候选去重。管理 API/UI 已支持 per_account 和逐凭据槽身份编辑。新增严格 item/私有引用归属、完整工具配对、旧历史迁移拒绝与完整前缀检查；数字精度修复用于身份处理。计费 settlement/reconcile 的短重试和错误保留已加入。

后续补齐实际本地 HTTP/原生 WS 两账号两成员、多轮 function/custom、A→B、owner 拒绝、结果重取
与新连接接手矩阵。修复 managed timeout Request 恢复、大整数 arguments、继承渠道加密与轮转主体；
加入完整结果加密保存、owner 范围幂等 claim、上下文预算、alias/affinity 授权、删除记录/导入导出、
批量删除、重加密及不重置线程的 profile 升级。并发登记、凭据 alias 和 claims 覆盖 MySQL
repeatable-read current-read；PostgreSQL 额度条件避免无类型数字参数。

配置/操作契约见 `docs/ACCOUNT_POOL.md`；最终命令、失败修复与证据边界见
`docs/account-pool-validation-20261116.md`。用户已追加授权提交、Actions 发布及服务器蓝绿切换，
源码已提交并通过完整 Actions，Release 镜像已上线；结果见
`../archive/2026-11-account-pool-release-deploy.md`，现有生产渠道配置保持原样。

最终通过全仓 Go test/vet/build、相关 race、四版本外部数据库、前端格式/lint/typecheck/
语义检查/240 测试/生产 build，以及改动 workflow 的 actionlint。失败及修复原因保留在验收记录。

用户新增要求：查看 Codex 官方源码并实现生成逻辑；已按上述固定源码实施，不能仅凭头部宣称完全官方传输模拟。用户尚未指定两账号/两成员实测资源。

后续真实门禁：指定授权的真实 A/B、U/V 和测试 PostgreSQL/keyring，验证供应商 quota、OAuth
rotation、两个独立服务进程的持久协调/接手、实际官方 TLS/ALPN/H2/长流对照、生产库存冲突、
备份恢复及灰度。测试设施与本地源码通过不能替代这些结果。保留 P5 独立可选范围：native
egress、目标自动 compact、私有资源迁移。旧多账号安装冲突按说明核实/拆分，不静默覆盖。

当前授权交付包含源码提交、镜像发布和服务器切换；不自动开启现有渠道的账号池模式。
完整功能任务因真实账号池验收待完成继续留在 active。
