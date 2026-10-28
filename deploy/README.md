# Deployment

这些文件是 Podman + systemd + Traefik 的参考模板，不是某台生产服务器的固定配置。真实域名、目录和凭证请放在服务器本地的 `/etc/search-gateway/deploy.env`，不要提交到仓库。

## 目录

- `ct100/container-search-gateway.service`：systemd 服务模板，`deploy.sh` 会替换 `@IMAGE@`、`@ROOT@`、`@DATA_DIR@`、`@CONFIG_FILE@`
- `ct100/traefik.yaml`：Traefik 动态配置模板，`deploy.sh` 会替换 `@DOMAIN@`
- `ct100/deploy.env.example`：服务器本地环境变量示例
- `ct100/deploy.sh`：安装并重启容器，最后检查 `/healthz`

## 首次部署

在目标服务器上准备配置和数据目录，权限按实际用户调整（示例使用 UID/GID 65532）：

```bash
sudo install -d -o 65532 -g 65532 -m 0755 /srv/search-gateway/data
sudo install -o 65532 -g 65532 -m 0600 /dev/null /srv/search-gateway/config.json
# 编辑 /srv/search-gateway/config.json；如果已有 store.json / gateway.db，一并放进 data 目录并 chown
```

复制部署环境变量并修改：

```bash
sudo install -d -m 0755 /etc/search-gateway
sudo cp deploy/ct100/deploy.env.example /etc/search-gateway/deploy.env
sudo chmod 0600 /etc/search-gateway/deploy.env
sudo editor /etc/search-gateway/deploy.env
```

`deploy.sh` 会读取：

| 变量 | 用途 | 默认值 |
| --- | --- | --- |
| `SEARCH_GATEWAY_ROOT` | 配置和数据所在根目录 | `/srv/search-gateway` |
| `SEARCH_GATEWAY_DATA` | 数据目录 | `$ROOT/data` |
| `SEARCH_GATEWAY_CONFIG` | 配置文件路径 | `$ROOT/config.json` |
| `SEARCH_GATEWAY_DOMAIN` | Traefik 路由域名 | `search.example.com` |
| `SEARCH_GATEWAY_TRAEFIK_DIR` | Traefik provider 目录 | `/etc/traefik/providers` |

然后执行：

```bash
sudo podman pull ghcr.io/myml/search-gateway:latest
sudo SEARCH_GATEWAY_DEPLOY_ENV=/etc/search-gateway/deploy.env \
  bash deploy/ct100/deploy.sh ghcr.io/myml/search-gateway:latest
```

脚本会安装 systemd unit 和 Traefik provider，重启 `container-search-gateway.service`，并轮询 `http://127.0.0.1:18787/healthz`。

## GitHub Actions

`.github/workflows/docker.yml` 在 `main` 推送和 `v*` tag 时测试、构建并推送镜像，不在公开仓库里自动部署生产服务器。部署由维护者用上面的 `deploy.sh` 手动执行，或者放在私有运维仓库里执行。

这样公开仓库的 CI 不需要自托管 runner，也不会因为 fork/PR 接触生产环境。
