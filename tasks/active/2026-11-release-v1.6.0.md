# Rillven v1.6.0 正式发布

Status: in_progress
Created: 2026-11-15

用户明确要求正式版，不要预发布版；沿用本会话 Actions → Releases → 服务器代理下载、
无缝切换和本次备份/中间文件清理的授权，仅在 main 维护。

基线：main `e1a8282`，线上已验证源码 `cb40c8f`，容器 `rillven-cb40c8ff5bda` / 3028。
现有产品版本 v1.5.1。本次包含新增兼容能力，发布 v1.6.0 / `rillven-v1.6.0`，
镜像 `rillven:1.6.0`，必须 prerelease=false、draft=false、Latest 指向正式标签。

更新 VERSION/CHANGELOG 及仓库 URL 大写 R，保持业务行为和历史标签/附件不变。
主分支发布准备提交跳过自动源码包，随后对正式 tag 手动触发原有完整 Actions 门禁；
手动正式标签执行不跳过质量、数据库、浏览器或镜像检查。

服务器沿用永久状态 keyring 和原配置，新版使用独立 project/容器/端口预热，
验证后 Caddy reload，排空旧请求，再清理本次中间文件和备份。
证据：`D:/Code/renewapi/audit/20261115-release-v1.6.0/`，不包含凭据或数据库副本。
