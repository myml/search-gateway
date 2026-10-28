# search-gateway

把多家搜索和网页抓取能力收进一个自托管网关。客户端只需要一个 API Key，背后的渠道切换、失败回退、结果聚合和调用统计都交给网关。

适合这些场景：

- 给 DSH、Agent 或内部服务提供统一的搜索 / 抓取接口
- 同时接入多个搜索提供商，按顺序回退或分摊流量
- 不想把 Tavily、Brave、Exa 等 Key 分散写在各个客户端里
- 需要一个能看调用日志、测试渠道、管理 API Key 的小型控制台

## 功能

- **多家搜索渠道**：Tavily、Brave、Exa、Serper、Bocha、SearXNG
- **网页抓取**：直接抓取或 Jina Reader
- **路由策略**：顺序回退、并发聚合去重、轮询/随机负载均衡
- **渠道月度额度**：为每个渠道设置每月调用次数上限，耗尽后自动跳过，次月自动恢复
- **API Key 管理**：每个 Key 分别绑定一条搜索路由和一条抓取路由
- **管理控制台**：渠道、路由、Key、调用日志、渠道测试
- **兼容接口**：REST、DeepSeek Search/Fetch API、Anthropic Messages、MCP
- **单二进制**：配置用 JSON，日志和统计用 SQLite，不依赖外部数据库

## 快速开始

### Docker

```sh
git clone https://github.com/myml/search-gateway.git
cd search-gateway

cp config.example.json config.json
# 编辑 config.json，至少设置 admin_tokens 和一个可用的搜索渠道

docker build -t search-gateway .
docker run -d --name search-gateway --restart unless-stopped \
  -p 127.0.0.1:8787:8787 \
  -v "$PWD/config.json:/etc/search-gateway/config.json:ro" \
  -v "$PWD/data:/data" \
  search-gateway
```

如果配置里使用 `api_key_env`，记得把对应环境变量传进容器，例如 `-e TAVILY_API_KEY=...`。

也可以直接使用构建好的镜像：

```sh
docker pull ghcr.io/myml/search-gateway:latest
```

如果镜像暂时不可匿名拉取，可以先 `docker login ghcr.io`，或者直接用上面的本地构建方式。

### 二进制

需要 Go 1.26 或更高版本：

```sh
go build -o search-gateway ./cmd/search-gateway
cp config.example.json config.json
./search-gateway -config config.json
```

启动后打开 <http://localhost:8787>。

## 配置

最小配置示例：

```json
{
  "listen": "127.0.0.1:8787",
  "admin_tokens": ["请换成一串足够长的随机值"],
  "search": [
    {
      "name": "tavily",
      "type": "tavily",
      "api_key_env": "TAVILY_API_KEY"
    }
  ],
  "fetch": [
    {
      "name": "direct",
      "type": "direct"
    }
  ]
}
```

[config.example.json](config.example.json) 里包含所有渠道的示例。常用字段：

| 字段 | 说明 | 默认值 |
| --- | --- | --- |
| `listen` | 监听地址 | `127.0.0.1:8787` |
| `admin_tokens` | 管理控制台和 admin API 的登录凭证 | 空 |
| `auth.tokens` | 旧版兼容：首次启动时种子成 API Key | 空 |
| `store_path` | 渠道 / 路由 / Key 的存储文件 | `./data/store.json` |
| `db_path` | 调用日志和统计的 SQLite 文件 | `./data/gateway.db` |
| `quota_timezone` | 渠道月度额度重置所用的自然月时区（IANA 名称） | `UTC` |
| `proxy` | 全局出站代理，支持 http/https/socks5 | 空 |
| `timeout_seconds` | 搜索超时 | `20` |
| `fetch_timeout_seconds` | 抓取超时 | `30` |
| `default_count` | 默认返回结果数，范围 1–50 | `8` |
| `user_agent` | 出站请求 User-Agent | `search-gateway/0.1` |
| `search[]` / `fetch[]` | 空库时导入的渠道 | 空 |

配置里的字符串支持 `${ENV}` 展开；渠道的 `api_key_env` 会从环境变量读取 Key。例如：

```sh
export TAVILY_API_KEY="tvly-..."
```

### 渠道类型

| 类型 | 角色 | 必需配置 |
| --- | --- | --- |
| `tavily` / `brave` / `exa` / `serper` / `bocha` | 搜索 | `api_key` 或 `api_key_env` |
| `searxng` | 搜索 | `base_url`，并且 SearXNG 需要开启 JSON 输出 |
| `direct` | 抓取 | 无 |
| `jina` | 抓取 | 可选 `api_key` |

渠道还可以单独设置 `base_url`、`proxy`、`timeout_seconds`、`headers` 和 `user_agent`。`direct` 默认使用浏览器 User-Agent；部分站点对非浏览器请求不友好时，可以改用 `jina` 或给渠道配置代理。

## 第一次使用

1. 启动服务，打开 <http://localhost:8787>。
2. 在登录页粘贴 `admin_tokens` 里的值。
3. 进入「搜索渠道 / 抓取渠道」，用页面上的「测试」确认上游可用。
4. 进入「API Key」，签发一个 Key。

Key 的明文只在创建时显示一次；服务端只保存哈希和展示前缀。API Key 会分别绑定一条搜索路由和一条抓取路由，搜索请求和抓取请求各走对应的路由。

### 两个凭证不要混

| 凭证 | 用途 |
| --- | --- |
| **Admin Token** | 登录控制台、调用 admin API、管理渠道 / 路由 / Key |
| **API Key** | 客户端调用搜索、抓取、Messages 和 MCP 接口 |

控制台顶部填的是 Admin Token，不是 API Key。

## 调用网关

所有客户端接口都需要 API Key：

```sh
export SG_KEY="sg_..."
```

也可以使用 `x-api-key: $SG_KEY`。

### 搜索

```sh
curl -s "http://localhost:8787/search?q=deepseek&count=5" \
  -H "Authorization: Bearer $SG_KEY"
```

返回：

```json
{
  "query": "deepseek",
  "route": "default",
  "channels": ["tavily"],
  "results": [
    {
      "title": "...",
      "url": "https://...",
      "snippet": "...",
      "date": null
    }
  ]
}
```

### 抓取网页

```sh
curl -s "http://localhost:8787/fetch?url=https%3A%2F%2Fexample.com" \
  -H "Authorization: Bearer $SG_KEY"
```

返回 `url`、`title`、`content`、`content_type`、`status_code`、命中的路由和渠道。

### 兼容接口

| 接口 | 说明 |
| --- | --- |
| `GET /search?q=&count=` | 搜索，curl 友好 |
| `GET /fetch?url=` | 抓取网页，curl 友好 |
| `POST /v1/tools/search` | DeepSeek Search API 兼容；请求体 `{"query":"...","count":5}` |
| `POST /v1/tools/fetch` | DeepSeek Fetch API 兼容；请求体 `{"url":"..."}` |
| `POST /v1/messages` | Anthropic Messages 形状的搜索专用接口 |
| `POST /mcp` | MCP Streamable HTTP，提供 `web_search` 和 `web_fetch` |

调用成功时会返回这些响应头：

- `X-Request-Id`：请求 ID
- `X-Search-Route`：命中的路由
- `X-Search-Channels`：实际使用的渠道
- `X-Search-Provider`：第一个渠道，兼容旧客户端

常见错误：无效 Key 返回 `401`；路由没有可用渠道或候选渠道的月度额度全部耗尽返回 `503`；渠道全部失败返回 `502`；参数错误返回 `400`。

## 在 DSH 中使用

先在控制台签发 API Key，然后在 DSH 的网页搜索设置里选择一种接入方式。

### 方式一：Messages API

设置 → 网页搜索 → **Messages API 基础地址**：

```text
http://localhost:8787/v1
```

API Key 填签发的 Key。

### 方式二：Web 搜索 API

打开「新版 Web 搜索 API」，**Search / Fetch API 基础地址**：

```text
http://localhost:8787/v1/tools
```

### 方式三：MCP

在 `@deepseek-ai/dsh-mcp-client` 配置里增加：

```yaml
- id: mcp-search
  name: '@deepseek-ai/dsh-mcp-client'
  config:
    serverName: search
    transport: streamable-http
    url: http://localhost:8787/mcp
    headers:
      Authorization: 'Bearer <YOUR_API_KEY>'
```

模型侧会看到：

- `mcp__search__web_search`
- `mcp__search__web_fetch`

MCP 端点是精简的无状态实现，不分配 session，也不推送 SSE。Key 同时绑定搜索和抓取路由时两个工具都可用；缺少哪条路由就只暴露另一个工具。

## 路由模式

一条路由由多个目标组成，目标可以是渠道，也可以是同角色的子路由（组）。

| 模式 | 行为 |
| --- | --- |
| `fallback` | 按顺序尝试，第一个成功即返回 |
| `merge` | 并发调用所有搜索目标，按 URL 去重后合并；部分失败不影响整体结果 |
| `balance` | 每次从轮询或随机起点开始，失败后继续尝试其它目标 |

补充规则：

- `max_retries` 限制失败后最多再尝试几个目标；`0` 表示不限制。
- `fallback_on_empty` 打开后，某个目标返回 0 条结果也会继续尝试下一个。
- `merge` 只对搜索路由有意义；抓取路由按顺序回退。
- 搜索路由只能引用搜索组，抓取路由只能引用抓取组；禁止循环引用。
- 删除仍被 API Key 引用的路由会失败，需要先调整 Key。

## 渠道月度额度

每个渠道可以设置每月调用次数上限，用来控制上游成本。额度按月自动重置，不需要额外的定时任务。

### 计数规则

- 单位是「次」，只统计真正发往上游的调用：搜索和抓取都算，成功和失败都算（失败同时计入错误数）。
- 同一个渠道的搜索和抓取共用一个额度，按渠道合计，不按 Key 分开计算。
- `monthly_quota` 为 `0` 或留空表示不限量；不限量时调用次数和错误数仍然照常统计。

### 达到额度后

- 路由引擎会自动跳过额度已耗尽的渠道，不会改动渠道的启停状态；`fallback`、`merge`、`balance` 和嵌套组的行为一致。
- 其它还有额度的渠道照常工作；如果一条路由的所有候选渠道都耗尽，接口返回 `503`，错误信息包含 `monthly quota exhausted`。
- 额度可以随时调大、调小或清零；调小到低于本月已用量时，该渠道会立即被视为耗尽。

### 自然月与时区

- 额度按 `quota_timezone` 配置的时区划分自然月（默认 `UTC`），跨月后自动使用新月份的计数，不需要重启或定时任务。
- 例如 `"quota_timezone": "Asia/Shanghai"` 表示按北京时间每月 1 日 0 点重置。

### 测试与额度

- 管理台的「测试渠道」直接调用上游，不受额度限制，也不计入用量。
- 「测试 API Key」会走正常路由，受额度限制，并计入用量。

### 配置与管理 API

渠道额度可以在管理台渠道编辑页设置，也可以直接调用管理 API：

```sh
curl -s -X PATCH http://localhost:8787/admin/api/channels/<channel-id> \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"monthly_quota": 1000}'
```

渠道对象会返回 `quota` 字段（`monthly_limit`、`used`、`errors`、`remaining`、`unlimited`、`exhausted`、`period`、`resets_at`）；需要清零当月用量时调用 `POST /admin/api/channels/{id}/quota/reset`。

全局时区写在配置文件里：

```json
{
  "quota_timezone": "Asia/Shanghai"
}
```

## 管理控制台

控制台是内置的单页应用，不需要单独部署前端，也不需要 npm。

- **概览**：渠道、路由、Key 数量，最近调用和渠道状态
- **搜索渠道 / 抓取渠道**：分别管理对应角色的上游，支持单个测试和批量测试；列表展示本月额度与用量，编辑页可设置月度额度、重置本月用量
- **搜索路由 / 抓取路由**：配置目标顺序和执行模式，支持嵌套组
- **API Key**：签发、启停、删除、查看用量，并可直接测试
- **调用日志**：按协议、状态、路由、Key 和关键字过滤，查看请求详情

控制台支持浅色/深色主题和移动端。

## 部署与安全

- 默认只监听 `127.0.0.1`，不会自动暴露到局域网或公网。
- 需要远程访问时，建议放在反向代理后面并启用 TLS；不要把管理面直接暴露在公网。
- `admin_tokens` 必须是强随机值，不能留空。
- `store.json` 里的渠道 Key 是明文（运行需要），`gateway.db` 里有 query、URL、来源 IP 和 UA；请把数据目录权限限制为服务用户可读。
- API Key 只保存 SHA-256 哈希，明文只在创建时出现一次。
- `direct` 抓取可以访问内网地址，存在 SSRF 风险；内网部署时请用网络策略限制可访问范围。
- SQLite 方案适合单实例部署；不要多个实例共享同一个数据库文件。

## 限制

- 不支持 SSE 流式输出。
- 支持渠道级月度调用次数额度，但不提供按 Key 的 QPS 或配额。
- `direct` 的正文提取是启发式清洗，复杂页面建议使用 `jina`。
- 日志目前没有自动按天清理，需要的话可以自行加定时任务。
- 配置仍然是 JSON 文件，适合中小规模单实例；更大规模建议换外部数据库和独立配置服务。

## 开发与贡献

开发、测试、项目结构和部署模板见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## License

[MIT](LICENSE)
