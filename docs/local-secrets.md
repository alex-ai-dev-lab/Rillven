# Local Secrets

Local credentials live outside the committed source in:

```text
D:\Code\renewapi\Token\github-auth.env
D:\Code\renewapi\Token\server-access.env
```

`github-auth.env` keys:

```text
GITHUB_USERNAME=
GITHUB_TOKEN=
GITHUB_REPO=alex-ai-dev-lab/rillven
GHCR_REGISTRY=ghcr.io
GHCR_OWNER=
GHCR_IMAGE=renewapi
```

`GHCR_*` 仅保留为旧工具的历史配置；当前构建通过 GitHub Releases 分发。
项目改名不移动本地凭据目录，既有凭据内容不随源码提交。

`server-access.env` keys:

```text
SERVER_HOST=
SERVER_PORT=22
SERVER_USER=
SERVER_PASSWORD=
SERVER_SUDO_PASSWORD=
SERVER_DEPLOY_DIR=
SERVER_COMPOSE_FILE=compose.yaml
SERVER_SERVICE_NAME=new-api
SERVER_HEALTHCHECK_URL=http://127.0.0.1:3002/healthz
SERVER_PROXY=http://127.0.0.1:10808
SSH_AUTH_METHOD=key
SSH_KEY_PATH=
```

Windows file permissions:

```powershell
icacls D:\Code\renewapi\Token /inheritance:r
icacls D:\Code\renewapi\Token /grant "$env:USERNAME:(OI)(CI)F"
```

Rotate tokens/passwords by updating the env files and revoking old credentials at the provider. Use GitHub Actions secrets only for CI-specific credentials; local scripts read only from `Token/`.
