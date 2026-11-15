# Rillven Actions 发布与无缝部署

Status: in_progress
Created: 2026-11-15

用户授权通过 GitHub Actions 构建镜像到 Releases，服务器通过代理下载，验证后无缝
切换；确认正常后删除本次中间文件与备份。源码仅在 main 维护，不清理历史 Releases/tags。

## Initial state

- main `1d9c3e51fb6e507171497a82521baebf5cf702ca`，本地干净。
- Actions `43689484454` 的四数据库及两组浏览器任务通过；quality 的 JSON 边界
  检查拒绝三个仅用于 RawMessage 的直接标准库导入，镜像/Release 尚未生成。
  改用既有 `common.RawMessage` 别名，保持门禁与数据类型不变，重新完整验证构建。
- 服务器当前容器 `new-api-v1-5-1`、镜像 `renewapi:1.5.1`、本机端口 3027，
  容器 healthy，Caddy active。其余运行配置、流量及数据库状态继续现场核对。

## Execution boundary

在服务器保留原实例并持续监测；核对 Release/源码/归档摘要与镜像身份，经在线数据库
备份和隔离副本迁移验证后启动独立 project/容器/端口，保持配置和数据兼容。
必要的独立状态 keyring 在服务器安全生成并保留，不能作为临时文件删除。
新身份/托管恢复的渠道开关沿用已有配置，不在部署过程中批量重置身份或打开实验功能。

候选通过真实协议与长流验收后 Caddy reload 切换；保留旧静态资源，等待旧连接排空，
完成公网验收后退役旧容器并清理本次临时文件/备份，随后再验证健康及入口。
证据保存于工作副本外 `D:/Code/renewapi/audit/20261115-deploy-rillven/`，不记录凭据。
