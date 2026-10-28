# Contributing

感谢参与 search-gateway。这个文档面向开发者和部署维护者；普通安装和使用请看 [README.md](README.md)。

## 开发环境

- Go 1.26+
- Docker / Podman（可选，用于构建镜像）
- Node.js（可选，仅用于检查前端 JS 语法）

```sh
git clone https://github.com/myml/search-gateway.git
cd search-gateway

cp config.example.json config.json
go run ./cmd/search-gateway -config config.json
```

默认监听 `127.0.0.1:8787`。前端资源已经通过 `go:embed` 打进二进制，不需要单独启动前端开发服务器。

## 项目结构

```text
cmd/search-gateway/    程序入口：读取参数、加载配置、启动 HTTP 服务
internal/config/       配置结构、默认值、环境变量展开和校验
internal/db/           SQLite 初始化、调用日志和统计写入
internal/logstore/     调用日志类型与旧 JSONL 日志导入
internal/routing/      路由执行引擎：fallback / merge / balance、嵌套组
internal/search/       搜索和抓取 provider、HTTP 公共逻辑、结果清洗
internal/server/       HTTP 路由、鉴权、管理 API、MCP、静态资源
internal/server/ui/    无构建前端：index.html / app.css / app.js
internal/store/        渠道、路由、API Key 的 JSON 持久化与迁移
deploy/                Podman / systemd / Traefik 部署模板
```

## 架构概览

### 启动流程

1. `cmd/search-gateway/main.go` 解析 `-config` / `-listen` 等参数。
2. `internal/config` 读取 JSON，展开 `${ENV}`，填默认值并校验 provider 类型。
3. `internal/store` 打开 `store_path`；文件不存在时用 `config.json` 里的 `search[]` / `fetch[]` / `auth.tokens[]` 做首次种子。
4. `internal/db` 打开 `db_path`，初始化 SQLite 表；必要时导入旧 JSONL 日志。
5. `internal/server` 注册客户端接口、管理接口和静态控制台，启动 HTTP 服务。

### 核心模型

- **Channel**：一个搜索或抓取上游，类型固定，配置包含 Key、Base URL、代理、超时、Headers、UA。
- **Route**：同一角色的渠道和/或子路由组成的有序目标列表，带执行模式和重试策略。
- **API Key**：同时绑定一条搜索路由和一条抓取路由；只存 SHA-256 哈希和展示前缀。
- **Call log**：记录协议、Key、路由、渠道、query/url、状态、耗时、来源 IP、UA、Request ID。

请求进入后，鉴权中间件解析 API Key 并选择对应角色路由；`internal/routing` 按路由模式调用 provider；结果统一写入 SQLite。

### 路由执行

- `fallback`：按目标顺序执行，跳过停用/不可用目标，第一个成功即返回。
- `merge`：并发调用全部搜索目标，按 URL 去重并按渠道顺序拼接；部分失败不阻塞结果。
- `balance`：按轮询或随机起点执行，失败后继续尝试后续目标。
- `max_retries`：限制失败后的额外尝试次数；`0` 表示不限制。
- `fallback_on_empty`：搜索返回空结果时是否继续尝试下一个目标。
- 组：父路由可以把同角色子路由当作目标；禁止自引用和循环。

抓取路由不支持 `merge`，但支持 fallback、balance 和嵌套组。

### MCP

`internal/server/mcp.go` 实现无状态的 MCP Streamable HTTP：

- `initialize` / `ping` / `tools/list` / `tools/call`
- 工具 `web_search`、`web_fetch`
- 对现代客户端的 `server/discover` 探测返回 `-32601`，引导客户端回退到 legacy initialize
- 不分配 session、不推送 SSE；每次 POST 独立鉴权
- 工具列表根据 Key 实际绑定的搜索/抓取路由过滤

## 测试

提交前至少运行：

```sh
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
node --check internal/server/ui/app.js
```

测试主要使用：

- 本地 fake SearXNG / fake HTML 上游
- `httptest` 启动完整服务栈
- 临时 store / SQLite 文件
- MCP、路由、配置、存储迁移的独立测试

修改存储结构、路由语义或鉴权逻辑时，请同时补测试。不要依赖真实外部搜索 API 做单元测试。

## 前端

前端在 `internal/server/ui/`，是原生 HTML/CSS/JS，没有构建步骤：

- `index.html`：页面骨架
- `app.css`：设计变量、布局、组件样式
- `app.js`：路由、API 调用、页面渲染

修改后重新 `go build` 即可，因为资源通过 `go:embed` 嵌入二进制。提交前运行：

```sh
node --check internal/server/ui/app.js
```

如果要做浏览器级检查，至少覆盖登录、渠道编辑、路由目标排序、API Key 测试、日志详情和移动端布局。

## 管理 API

管理面使用 Admin Token，支持 `Authorization: Bearer <token>` 或 `x-api-key`。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/admin/api/overview` | 计数、store 路径、渠道/路由/Key 明细与用量 |
| GET/POST | `/admin/api/channels` | 列表 / 新建（新建前会构建一次 provider 校验配置） |
| PATCH/DELETE | `/admin/api/channels/{id}` | 修改 / 删除（删除会清理路由引用） |
| POST | `/admin/api/channels/{id}/test` | 测试单个渠道，body 为 `{"query":"..."}` 或 `{"url":"..."}` |
| POST | `/admin/api/channels/test` | 并发测试渠道；可传 `role`、`query`、`url`、`channel_ids` |
| GET/POST | `/admin/api/routes` | 列表 / 新建 |
| PATCH/DELETE | `/admin/api/routes/{id}` | 修改 / 删除（被 Key 引用时返回 409） |
| GET/POST | `/admin/api/keys` | 列表 / 签发（响应里的 `token` 只出现一次） |
| PATCH/DELETE | `/admin/api/keys/{id}` | 改名称/路由/启停 / 删除 |
| POST | `/admin/api/keys/{id}/test` | 按该 Key 的路由真实执行搜索和抓取 |
| GET | `/admin/api/logs` | 过滤参数：`protocol`、`status`、`key_id`、`route_id`、`channel`、`q`、`since`、`until`、`limit`、`offset` |
| POST | `/admin/api/logs/clear` | 清空调用日志 |

## 存储与数据

`store_path` 是配置文件式存储，包含渠道、路由、API Key；写入采用临时文件 + rename，权限 `0600`。

`db_path` 是 SQLite，主要表：

| 表 | 内容 |
| --- | --- |
| `call_logs` | 搜索、抓取、Messages、MCP、渠道测试、鉴权失败 |
| `channel_stats` | 每个渠道的请求数、错误数、最近错误、最近使用时间 |
| `channel_tests` | 每个渠道按角色的最近测试结果 |
| `key_stats` | 每个 API Key 的请求数、错误数、最近使用时间 |

常用排查 SQL：

```sh
sqlite3 data/gateway.db

.tables
SELECT protocol, COUNT(*) FROM call_logs GROUP BY protocol;
SELECT status, COUNT(*) FROM call_logs WHERE time >= datetime('now','-1 day') GROUP BY status;
SELECT channel_id, search_requests, search_errors, fetch_requests, fetch_errors FROM channel_stats ORDER BY search_requests DESC;
SELECT key_id, requests, errors FROM key_stats ORDER BY requests DESC;
```

### 迁移注意事项

- 旧版本的单路由会自动拆成搜索路由和抓取路由，已有 Key 自动绑定两者。
- `generic` 自定义协议渠道已移除；旧 store 中的 `generic` 渠道会在启动时删除，并从路由目标中移除。旧 `config.json` 中的 `type: "generic"` 会直接导致启动校验失败。
- 旧 JSONL 日志会在 `call_logs` 为空时导入 SQLite；导入逻辑见 `internal/logstore`。

## 构建与发布

```sh
docker build -t search-gateway .
```

GitHub Actions 在 `main` 推送和 `v*` tag 时运行格式检查、vet、race test，并构建镜像。镜像标签包括 `latest`、`sha-<short>`、分支/PR tag 和 semver tag。

Release 前确认：

- 所有测试通过
- 配置和迁移行为有测试覆盖
- 没有把真实域名、IP、Token、Key 或生产数据提交进仓库
- 文档和示例配置可执行

## 部署模板

`deploy/` 下是 Podman + systemd + Traefik 的参考模板，不应该把生产环境的真实域名、路径和凭证写进公开仓库。`deploy/ct100/deploy.env.example` 是服务器本地配置示例；复制到 `/etc/search-gateway/deploy.env` 后按自己的环境替换，该文件已经被 `.gitignore` 排除。

部署前至少确认：

- 反向代理终止 TLS，管理面不直接暴露公网
- 数据目录和配置文件只允许服务用户读写
- `admin_tokens` 使用强随机值
- 容器健康检查访问 `/healthz`
- 对 `direct` 抓取做网络访问限制

## 提交 Pull Request

1. 从 `main` 创建分支。
2. 修改代码时优先补测试。
3. 运行上面的格式、vet、test 和前端语法检查。
4. 在 PR 描述里说明行为变化、配置变化和迁移影响。
5. 不要提交 `config.json`、`data/`、日志、数据库或任何真实凭证。

## 安全

- 不要用公开 issue 报告未修复的安全问题。
- 涉及 SSRF、鉴权绕过、密钥泄露的改动请在 PR 描述里明确说明风险和验证方式。
- 渠道配置里的上游 Key 会明文存在 `store.json`；这是运行所需，但必须限制文件权限。

## License

MIT，见 [LICENSE](LICENSE)。
