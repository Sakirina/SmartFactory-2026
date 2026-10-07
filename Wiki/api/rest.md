# HTTP API 与可恢复订阅

[Wiki 首页](../README.md) · [数据契约](data-contract.md)

业务接口使用 `/api/sf/v1`，请求和响应采用 JSON，每次调用检查当前身份、资源、角色与业务版本。公开 [OpenAPI](../../contracts/1.0/openapi.json) 保存方法和结构，在线 `/api/sf/v1/contracts` 提供契约目录，具体行为可从文末的路由与应用源码核对。

## 登录和读取身份

下面使用本机演示地址，密码和令牌占位符替换为当前账号资料。HTTPS 部署使用实际入口，并通过 curl 的 `--cacert` 指定客户 CA。

```sh
SF_BASE_URL='http://127.0.0.1:8090'
curl --fail-with-body -sS \
  -H 'Content-Type: application/json' \
  --data '{"login":"viewer","password":"REPLACE_WITH_ACCOUNT_PASSWORD"}' \
  "$SF_BASE_URL/api/sf/v1/login"

SF_TOKEN='REPLACE_WITH_RETURNED_TOKEN'
curl --fail-with-body -sS \
  -H "Authorization: Bearer $SF_TOKEN" \
  "$SF_BASE_URL/api/sf/v1/me"
```

登录返回 `token` 与 `user`，后续传入 Bearer 请求头。现场二次认证采用登录 `code` 字段，退出调用 `POST /logout`；身份失效后清除当前结果并重新认证。

## 服务端查询和续页

`POST /queries/{kind}` 的 `kind` 为 `entities`、`alarms`、`executions` 或 `trend`。以下生成最近一小时的固定请求，保存 `trend-query.json` 供查询和订阅共用：

```sh
python3 - <<'PYQUERY'
import json, time
from pathlib import Path
now_ms = time.time_ns() // 1_000_000
query = {
    "resource_ids": ["climate-1"],
    "keys": ["temperature"],
    "from_ms": now_ms - 3_600_000,
    "to_ms": now_ms,
    "resolution": "raw",
    "limit": 100
}
Path("trend-query.json").write_text(json.dumps(query) + "\n")
PYQUERY
curl --fail-with-body -sS \
  -H "Authorization: Bearer $SF_TOKEN" \
  -H 'Content-Type: application/json' \
  --data-binary @trend-query.json \
  "$SF_BASE_URL/api/sf/v1/queries/trend"
```

| 请求字段 | 适用条件 |
| --- | --- |
| `resource_ids` | 当前可读资源标识数组，最多 256 项 |
| `keys`、`resolution` | trend 测点数组及 `raw`／`minute`／`hour`／`day` |
| `from_ms`、`to_ms` | trend 明确的包含端点毫秒范围，最多三个 366 天 |
| `limit` | 默认 100，trend 最大 2000，其他查询最大 500 |
| `page_token` | 上页的 `next_page_token`，保留原查询和身份 |
| `entity_kind`、`search` | 实体种类和搜索 |
| `active`、`acknowledged`、`assignee_id`、`handling_status` | 告警状态与处置筛选 |
| `definition_id`、`status` | 相应业务对象的定义及状态 |

响应 `QueryPage` 包含 `items`、`scope`、`snapshot_cursor`、`has_more`、`next_page_token` 和 `metadata`。客户端持续读取到 `has_more` 为 false，保留服务返回的游标字符串；续页使用同一筛选、时间范围和身份，权限或数据代际改变后按返回原因重新开始查询。

`metadata.quality_scope` 描述质量统计范围，缺口、修订和来源与数值一起解释。`GET /data` 等已有读取入口继续提供兼容结构，其 `DataResult` 字段见[数据契约](data-contract.md)。

## 可恢复 SSE

`POST /queries/{kind}/events` 使用相同请求体，返回 `text/event-stream`。客户端保存最后完整事件的 `id`，断连后使用原请求及 `Last-Event-ID` 恢复。

```sh
curl --no-buffer --fail-with-body -sS \
  -H "Authorization: Bearer $SF_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  --data-binary @trend-query.json \
  "$SF_BASE_URL/api/sf/v1/queries/trend/events"
```

| 事件 | 客户端行为 |
| --- | --- |
| `snapshot` | 建立当前窗口和元数据 |
| `delta` | 按身份及修订应用新增、变化、移除和窗口顺序 |
| `checkpoint` | 保存已提交游标 |
| `reset` | 清除受影响窗口，按原因重新查询或认证 |

订阅维护当前身份下前 `limit` 行，续页查询另行使用快照。游标与查询、身份、授权和数据代际绑定；服务发送心跳，缓冲或恢复条件改变时返回重建要求。`GET /events` 使用同一订阅机制，已有客户端也读取上述事件类型。

## 对象、动作和接口目录

对象更新使用 `expected_version`，创建为 0，修改为上次读取版本。告警、控制、任务、模板及发布动作还使用各自的请求身份、已阅版本或代次；遇到冲突时读取当前对象、保留修改并检查允许动作，再提交。

| 领域 | 主要路径，均加业务前缀 |
| --- | --- |
| 设备与协议 | `/entities`、`/asset-proposals`、`/device-protocols`、`/devices/{id}/configuration`、`/connector-configurations` |
| 定义与评审 | `/drafts`、`/rule-nodes`、草稿 `validate`／`simulate`／`semantic-diff`／`impact`／`publish`、定义 `versions`／`rollback` |
| 告警与工单 | `/alarms/{id}/actions`、`/work-orders`、工单 `actions`／`handovers` |
| 控制 | `/executions`、`approve`／`dispatch`／`reconcile`／`resume`／`cancel`、`/control-operations/{id}` |
| 历史与任务 | `/analysis-runs`、`/shadow-candidates`、`/jobs`、`/tasks`、`/task-queues` |
| 模板与发布 | `/scene-templates`、`/template-batches`、`/release-artifacts`、`/releases`、`/release-deployments` |
| 节点配置 | `/workload-identities`、`/configuration-reports`、`/configuration-runtime` |
| 管理与审计 | `/organization`、`/users`、`/grants`、`/config`、`/plugins`、`/audit` |
| 页面与 AI | `/dashboards`、`/notifications`、`/assistant`、`/investigations` |

健康检查为 `GET /health`，MCP 为 `/mcp/read` 和 `/mcp/draft`，说明见[AI 指南](../user-guide/assistant.md)。服务通信及权威内部接口采用独立身份和 TLS，集成客户端按公开业务接口工作。

## 响应与错误

| 状态 | 处理内容 |
| --- | --- |
| 202 | 业务已受理，按原请求身份继续查询实际执行或同步结果 |
| 400／422 | 核对 JSON、字段、预算及当前业务条件，读取响应中的字段问题 |
| 401／403 | 处理会话失效或当前许可拒绝，清除失去访问权的内容 |
| 404 | 核对对象、版本和路径 |
| 409 | 读取最新业务版本、筛选或授权，重新核对本次操作 |
| 429 | 按限制窗口重新执行登录或请求 |
| 503／504 | 核对依赖可用性、超时与服务日志 |

不同路由的错误结构见 OpenAPI，客户端保留 `detail`、`errors` 或 `error` 提供的内容。普通业务正文预算通常为 2 MiB，程序工件上传采用独立最大 256 MiB 的路径；具体结构和请求预算按对应接口检查。

本页依据：[类型化查询路由](../../Platform/internal/api/query_routes.go)、[查询应用](../../Platform/internal/application/queries.go)、[业务路由](../../Platform/internal/api/server.go)、[生成客户端](../../Frontends/src/generated-client.ts)、[查询契约](../../contracts/1.0/QueryPage.schema.json)。
