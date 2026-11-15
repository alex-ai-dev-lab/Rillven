# Rillven 项目改名

Status: complete
Created: 2026-11-15

## Goal

用户要求将项目改名为 Rillven，完成检查后只提交主分支；进一步明确 GitHub 仓库也改为 `alex-ai-dev-lab/rillven`。

## Scope and decisions

- 在 `repair-20261017` 的 main 工作树执行，上一轮已验证修复与改名单独提交；不创建工作分支。
- 更新界面/默认站点名、CLI、文档、仓库链接和新构建/镜像/产品标签命名。
- 保留已发布的 RenewAPI 标签及附件、原作者署名；内部持久/加密命名空间、已有部署数据路径和兼容配置不能机械替换。
- 新配置使用 RILLVEN 前缀并兼容旧 RENEWAPI/NEWAPI 入口；状态加密 keyring 按一组读取，避免新旧字段混用。
- 完成源码、浏览器与构建/发布元数据验证后，再执行仓库改名及主分支提交核对。生产不在本次部署范围。

## Result

- 前一轮修复已单独提交为 `75717ba`；改名为本归档所属的 main 提交。
- GitHub 改名完成：`https://github.com/alex-ai-dev-lab/rillven`，repo ID
  `1234291482`、默认分支 main 不变；origin 已更新，旧网页地址返回 301 到新地址。
  改名前后 `git ls-remote --refs` 完全一致，没有重写历史分支或标签。
- 新 CLI / UI / 导出文件、发布/镜像命名及文档统一，详见 ADR-024。
  整组 keyring 选择、Compose 传参、部署和回滚助手同时覆盖新旧 IMAGE 配置。
- 检查期间补齐此前新增文件的版权头；沿用编辑草稿与异步服务器配置同步行为，
  为局部 effect lint 例外注明理由，并将两处表单保存回调延迟至事件执行。

## Validation

- 全仓 `go test ./...`、`go vet ./...`、`go build ./...` 通过。
- 密钥旧变量/新变量迁移、旧密文可读、轮换、非空新配置优先、禁止字段混用及
  不完整配置不复用缓存通过；受影响 common 测试 race 通过。
- 前端 240 项测试、500 个断言通过；类型、语义 lint、两笔提交全部改动文件
  lint/Prettier、版权检查、冻结锁文件安装与生产构建通过。
- 发布元数据 4 组测试、PowerShell/Bash 产品标签 prepare 校验与语法检查、
  actionlint 通过；没有实际创建产品标签。
- 使用官方 Docker Compose v2.40.3（下载 SHA256 已核对）实际解析两套 Compose：
  默认、旧变量、新旧并存共六组通过；容器/网络/挂载与基线一致，keyring 分别传入。
- 隔离 SQLite + Playwright 十组验收通过：默认名/仓库链接、旧默认值、自定义名、
  接口暂时不可用时的旧缓存和页面 title/meta、零价、改名/导出后保存、500 草稿重试、
  双击/碰撞/编辑切换、四种视口，以及独立渠道身份/恢复/出口/队列真保存。
  首次测试暴露页面标题独立 status 缓存未规范化，修复后完整重跑通过。

证据在 `D:/Code/renewapi/audit/20261115-rillven-*.log`、
`20261115-rillven-browser-final/report.json`、`rillven-tools/compose-validation.json`
及 `rillven-refs-before.txt` / `rillven-refs-after-rename.txt`。
本机未启动 Docker 引擎；镜像双架构构建和容器内符号链接验收由已有 CI 执行。
本次不部署生产；主分支推送仅触发现有检查和源码预发行包流程。
