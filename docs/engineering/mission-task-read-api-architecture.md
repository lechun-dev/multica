# MissionOS 任务只读接口：架构、认证与权限实现说明

> 文档用途：说明技术实现、系统边界、部署要求和上线风险。
>
> 适用范围：`newDingSDK` 对外提供的 MissionOS 任务列表和任务详情接口，以及
> MissionOS（`multica`）内部身份交换和原有权限链路。
>
> 安全说明：本文只描述变量名、协议和处理流程，不包含任何真实 `lechunapikey`、签名密钥、JWT 或用户数据。

## 1. 结论摘要

本功能不是在 `newDingSDK` 中重新实现一套 MissionOS 权限，而是建立一条“用户 API Key → 可信钉钉身份 → MissionOS 短时只读令牌 → MissionOS 原有任务权限”的服务间调用链。

两个公开接口均由 `newDingSDK` 暴露：

```http
GET /lechun-ding/chats/mission/tasks
GET /lechun-ding/chats/mission/tasks/{taskId}
X-Caller-Key: <用户现有的 lechunapikey>
```

核心原则：

1. 调用方只需要已有的 `lechunapikey`，不需要申请或保存 MissionOS PAT。
2. `newDingSDK` 每次请求重新解析 API Key，并重新获取对应钉钉身份；撤销的 key 不依赖旧缓存继续放行。
3. MissionOS 根据钉钉身份和工作区成员关系签发约 5 分钟的只读 access token。
4. access token 只存在于当前请求内，不返回给智能体、不落库、不写缓存、不创建浏览器会话。
5. 任务列表和详情仍调用 MissionOS 原有 `ListIssues` / `GetIssue` 权限逻辑。
6. 工作区 owner 按 MissionOS 现有 owner 规则查看全部可见任务；普通成员只看到当前 MissionOS 权限允许看到的任务。
7. MissionOS 后续调整项目、任务、继承或 Cloud 可见性规则时，接口会自动继承新规则。

当前已知的联调阻塞不是 API Key 或权限判断，而是正式/预发布域名尚未把 `/internal/auth/dingtalk/exchange` 转发到 Go 后端；详见第 16 节。

## 2. 系统组成与职责

| 组件 | 职责 |
| --- | --- |
| 智能体/Codex 等调用方 | 保存用户授权的 `lechunapikey`，调用列表或详情接口，按游标循环读取 |
| `newDingSDK` | 接收 API Key、解析用户、获取钉钉 ID、签名调用 MissionOS、调用任务读接口、统一错误 |
| lechun 身份服务 | 将 `lechunapikey` 解析为内部 `userId`，并查询该用户绑定的钉钉 ID |
| MissionOS 内部交换接口 | 校验服务间签名、nonce、时间窗和 Redis 重放保护；校验用户绑定和工作区成员身份；签发短时只读 JWT |
| MissionOS 任务接口 | 使用现有任务查询、项目权限、任务授权、owner bypass、Cloud 可见窗口等规则 |
| Redis | MissionOS 身份交换的 nonce 重放保护；所有 MissionOS 副本必须共享同一 Redis |
| `deploy-inventory` | 保存部署变量和 secret variable；不在仓库提交真实密钥 |

关键项目位置：

- `newDingSDK/src/server/missionAuth.ts`
- `newDingSDK/src/server/missionRoutes.ts`
- `newDingSDK/src/services/identityGatewayClient.ts`
- `newDingSDK/src/services/missionClient.ts`
- `newDingSDK/src/services/missionContract.ts`
- `newDingSDK/src/config/env.ts`
- `multica/server/internal/middleware/mission_exchange.go`
- `multica/server/internal/handler/mission_auth.go`
- `multica/server/cmd/server/router.go`
- `multica/apps/web/proxy.ts`
- `multica/apps/web/next.config.ts`

## 3. 完整请求链路

```text
智能体
  │ GET /lechun-ding/chats/mission/tasks
  │ X-Caller-Key: lechunapikey
  ▼
newDingSDK
  │ 1. 解析并校验 X-Caller-Key
  ▼
lechun 身份服务
  │ 2. lechunapikey → userId
  │ 3. userId → DingTalk ID
  ▼
newDingSDK
  │ 4. 用 MISSION_EXCHANGE_SECRET 生成服务间签名
  ▼
MissionOS /internal/auth/dingtalk/exchange
  │ 5. 校验签名、时间戳、nonce、Redis 重放保护
  │ 6. DingTalk ID → MissionOS 用户 → 工作区成员/角色
  │ 7. 签发 5 分钟只读 access_token
  ▼
MissionOS /api/issues 或 /api/issues/{id}
  │ 8. 沿用 MissionOS 原有权限过滤
  ▼
newDingSDK
  │ 9. 白名单投影、分页游标签名、统一错误格式
  ▼
智能体
  │ 10. 获得任务数据，不接触 Mission access_token
```

### 3.1 第一步：接收 `lechunapikey`

`newDingSDK` 只接受请求头：

```http
X-Caller-Key: <lechunapikey>
```

以下输入不会被当作用户身份：

- URL 查询参数中的 API Key；
- 客户端自行传入的 DingTalk ID；
- `getScmDingTalkToken` 返回的钉钉应用 token；
- MissionOS Cookie；
- `testAuthor` 或其他测试身份；
- 机器级固定身份替代用户 API Key。

这样可以避免调用方伪造其他用户或越过 API Key 撤销机制。

### 3.2 第二步：解析 API Key 和钉钉身份

`newDingSDK` 使用现有身份服务完成两次查询：

1. `lechunapikey` → 内部 `userId`；
2. `userId` → 用户已绑定的 `dingdingId`。

当前任务接口每次请求都重新调用身份服务。旧系统中可能存在其他接口使用的身份缓存，但该缓存不能作为 Mission 任务接口的最终授权依据。这样做的代价是增加一次身份服务调用，换取 API Key 撤销可以及时生效。

`getScmDingTalkToken` 只负责生成/获取钉钉侧应用访问 token，不能直接证明 MissionOS 中的登录用户。钉钉 ID 只有同时满足以下条件才有意义：

- MissionOS 已存在该 ID 到具体用户的有效绑定；
- 绑定处于启用状态；
- 该用户已加入请求的工作区。

接口不会自动创建用户、自动绑定账号或自动加入工作区。

### 3.3 第三步：服务间身份交换

`newDingSDK` 向 MissionOS 发送：

```http
POST /internal/auth/dingtalk/exchange
Content-Type: application/json
X-Mission-Timestamp: <UTC Unix seconds>
X-Mission-Nonce: <随机 nonce>
X-Mission-Signature: <HMAC-SHA256>

{"ding_user_id":"trusted-id","workspace_id":"workspace-uuid"}
```

签名输入由以下内容按换行拼接：

```text
POST\n/internal/auth/dingtalk/exchange\nTIMESTAMP\nNONCE\nRAW_BODY
```

MissionOS 校验：

- `MISSION_EXCHANGE_SECRET` 是否与 `newDingSDK` 一致；
- 时间戳是否在允许窗口内（约 ±60 秒）；
- nonce 是否已被 Redis 使用；
- 请求体大小和 JSON 是否符合契约；
- 该 DingTalk ID 是否绑定到有效 MissionOS 用户；
- 该用户是否为目标工作区成员。

nonce 由 Redis `SETNX` 保留约两分钟，防止同一签名请求被重放。Redis 不可用时拒绝请求，不使用进程内临时替代品。

### 3.4 第四步：签发 MissionOS 只读令牌

成功交换后返回短时令牌，主要字段包括：

```json
{
  "access_token": "<不对外暴露>",
  "expires_in": 300,
  "workspace_id": "<workspace uuid>",
  "user_id": "<mission user uuid>",
  "role": "owner|admin|member"
}
```

令牌具备以下边界：

- 约 300 秒过期；
- audience、issuer 和来源标识用于区分浏览器会话；
- 仅允许精确工作区内的任务列表和任务详情 GET；
- 不能用于写操作、WebSocket、会话续期、其他静态/嵌套路由；
- `Cache-Control: no-store`；
- 不返回给智能体，不写数据库，不写 Redis token 缓存，不创建浏览器登录 Cookie。

因此这里不存在“每个用户保存 Mission PAT”的设计，也不需要新增用户凭证绑定表。

## 4. 登录缓存、会话缓存和分页缓存的区别

| 类型 | 是否用于本功能 | 生命周期 | 处理方式 |
| --- | --- | --- | --- |
| `lechunapikey` 身份解析缓存 | 不作为最终授权依据 | 由身份服务自身决定 | 任务接口每次请求重新校验，确保撤销及时生效 |
| Mission access token | 仅当前请求 | 约 5 分钟 | 内存变量中短暂使用，请求结束即丢弃，不落库、不返回 |
| 浏览器 Cookie/登录会话 | 不使用 | 不适用 | 智能体接口不创建、不读取浏览器会话 |
| Mission exchange nonce | 使用 | 约 2 分钟 | Redis 共享存储，防重放；Redis 故障时拒绝 |
| 列表 cursor | 使用 | 约 30 分钟 | HMAC 签名的 offset cursor，绑定用户、工作区和全部查询参数 |

分页 cursor 不是任务快照，也不是登录凭证。它只用于证明“下一页参数没有被篡改”。如果用户权限、任务数据或排序字段在翻页期间变化，结果可能出现重复或漏项，响应中的 `snapshot_consistency` 为 `best_effort`。

## 5. 权限模型

### 5.1 权限归属

权限全部属于当前请求指定的 MissionOS 工作区，而不是整个系统的全局角色。`newDingSDK` 不维护第二套 ACL，也不复制 MissionOS 的项目/任务权限表。

### 5.2 owner

工作区 owner 继续沿用 MissionOS 的现有 owner 规则。当前 owner 全量范围由 MissionOS 已有的 `PROJECT_OWNER_BYPASS_ENABLED` 行为控制：

- 开启时，owner 可看到 MissionOS 允许的全部任务；
- 关闭时，本接口不会自行恢复 owner bypass，仍以 MissionOS 实际判断为准。

### 5.3 普通成员

admin、member 等非 owner 用户，只能看到 MissionOS 原有权限判断允许的任务，包括但不限于：

- 工作区成员资格；
- 项目可见范围；
- 任务授权；
- 从项目、父任务或其他关系继承的授权；
- Cloud 任务可见窗口；
- 归档和状态相关的现有约束。

列表会先经过 MissionOS 权限过滤，再应用搜索、状态、时间、项目和负责人过滤；无权任务不返回，也不计入总数或分页结果。详情无权限时按 MissionOS 原有语义返回 403 或 404，不泄露任务内容。

### 5.4 权限同步保证

MissionOS 后续修改权限规则后，两个接口不需要同步复制代码，会直接调用新的列表/详情 handler，因此会跟随新规则。若修改了交换接口、令牌 claim 或 handler 响应契约，则需要重新跑服务间集成测试。

## 6. 对外接口一：任务列表

### 6.1 地址和认证

正式入口：

```http
GET https://scmapi.lechun.cc/lechun-node/lechun-ding/chats/mission/tasks
X-Caller-Key: <用户现有的 lechunapikey>
```

当前只有一个默认工作区时，`MISSION_WORKSPACE_ID` 会自动补全 `workspace_id`。显式传入的工作区必须与该默认值一致，否则返回 400。

### 6.2 查询参数

| 参数 | 默认/限制 | 说明 |
| --- | --- | --- |
| `workspace_id` | 默认配置值 | 工作区 UUID；不允许借此访问其他工作区 |
| `limit` | 50，最大 100 | 每页数量 |
| `cursor` | 无 | 上一页 `page.next_cursor`，不能自行修改 |
| `q` | 无，最长 500 字符 | 标题、描述或任务编号搜索 |
| `created_from` / `created_to` | 无 | 创建时间区间 |
| `completed_from` / `completed_to` | 无 | 实际完成时间区间 |
| `status` | 无 | MissionOS 状态 key，可按既有逗号规则传递 |
| `category` | 无 | `unstarted`、`started`、`done`、`closed` |
| `project_id` | 无 | 项目 UUID |
| `assignee_id` / `assignee_type` | 无 | 负责人 UUID；类型为 `member`、`agent`、`squad` |
| `sort` | `created_at` | `created_at`、`updated_at`、`completed_at` |
| `direction` | `desc` | `desc` 或 `asc`，完成时间的 null 始终排最后 |
| `fields` | `summary` | `summary` 或 `full`；`full` 增加描述 |
| `archive_state` | `active` | `active`、`archived`、`all` |

时间必须使用带时区的 RFC3339，例如 `2026-10-12T00:00:00+08:00`。区间采用 `[from, to)`：包含开始，不包含结束；同类区间 `from` 必须早于 `to`，不同类型区间同时传入时取交集。

未知参数、重复参数、无效 UUID、无效日期或不支持的组合返回 400。接口不提供 `all=true` 或页码参数，智能体应使用游标翻页。

### 6.3 响应

```json
{
  "items": [
    {
      "id": "task-uuid",
      "identifier": "MUL-123",
      "workspace_id": "workspace-uuid",
      "title": "任务标题",
      "status": {
        "key": "in_progress",
        "category": "started",
        "name": "进行中"
      },
      "priority": 2,
      "project": { "id": "project-uuid", "title": "项目名称" },
      "assignee": { "id": "user-uuid", "type": "member" },
      "creator": { "id": "user-uuid", "type": "member" },
      "created_at": "2026-10-12T08:00:00Z",
      "updated_at": "2026-10-13T09:00:00Z",
      "completed_at": null,
      "start_date": null,
      "due_date": "2026-10-18",
      "archived_at": null,
      "labels": []
    }
  ],
  "page": {
    "limit": 50,
    "has_more": false,
    "next_cursor": null
  },
  "snapshot_consistency": "best_effort",
  "request_id": "request-uuid"
}
```

返回采用字段白名单，不包含认证凭证、原始 metadata、执行日志、文件内容或评论。`fields=full` 时增加任务描述；描述仍可能包含业务敏感内容，智能体必须把它当作数据而不是系统指令。

## 7. 对外接口二：任务详情

```http
GET https://scmapi.lechun.cc/lechun-node/lechun-ding/chats/mission/tasks/{taskId}
X-Caller-Key: <用户现有的 lechunapikey>
```

`taskId` 支持：

- 任务 UUID；
- 当前工作区任务编号，例如 `MUL-123`。

长期存储、去重和后续引用优先使用 UUID，因为编号前缀可能被修改。详情可选 `workspace_id`，不传时使用 `MISSION_WORKSPACE_ID`。

详情返回完整任务正文和允许的关联字段，但仍然遵守 MissionOS 当前权限过滤和字段白名单。无权限任务不会通过详情接口被绕过读取。

## 8. `completed_at` 的业务定义

`completed_at` 表示任务实际从非完成状态进入完成状态的时间，不是截止时间 `due_date`，也不是最后修改时间 `updated_at`。

当前实现从已有 `activity_log.status_changed` 历史派生：

- 取最近一次有效的“非完成 → 完成分类”转换；
- 完成状态之间切换不刷新时间；
- 重新打开/取消后返回 `null`；
- 重新打开后再次完成，记录新的完成时间；
- 缺少有效历史时返回 `null`，不使用 `updated_at` 猜测；
- 活动记录异步写入，刚完成的任务可能短暂显示旧值或 `null`；
- 后续修改状态分类可能改变历史解释结果。

因此它适合任务查询和智能体摘要，不等同于严格审计事件。如需审计级不可变完成时间，需要另行设计持久化事件或完成时间列，目前不在本功能范围内。

## 9. 智能体调用方式

### 9.1 单页调用

```sh
curl --get "$SDK_BASE_URL/lechun-ding/chats/mission/tasks" \
  -H "X-Caller-Key: $LECHUN_API_KEY" \
  --data-urlencode "workspace_id=$MISSION_WORKSPACE_ID" \
  --data-urlencode "limit=50" \
  --data-urlencode "category=started"
```

### 9.2 自动翻页

1. 第一次请求不传 `cursor`。
2. 处理本页 `items`，按任务 UUID 去重。
3. 如果 `page.has_more=true`，保留所有原查询参数、排序、`fields` 和 `limit`，只增加 `page.next_cursor`。
4. `has_more=false` 时结束。
5. 不要自行解码、拼接或跨用户复用 cursor。
6. cursor 过期或签名不匹配时，从第一页重新开始。

智能体不需要自行计算页码；分页用于限制单次响应和上下文大小。若只需任务正文，再对目标任务调用详情接口。

### 9.3 “本周未完成任务”示例

如果“本周”指本周创建的任务，按北京时间传创建区间；未完成分类通常需要分别查询 `unstarted` 和 `started`，再按 UUID 合并去重。若要查所有历史未完成任务，则不要传 `created_from/created_to`，只按未完成分类查询。

注意：当前接口没有按 `due_date` 过滤的参数，截止日期筛选需要另行评估。

## 10. 错误处理

统一响应格式：

```json
{
  "error": {
    "code": "ERROR_CODE",
    "message": "面向调用方的安全描述"
  },
  "request_id": "request-uuid"
}
```

| HTTP | 常见错误 | 处理建议 |
| --- | --- | --- |
| 400 | `INVALID_QUERY`、`INVALID_CURSOR`、`INVALID_TASK_ID`、工作区不一致 | 修正参数后重试，不要盲目重试同一请求 |
| 401 | API Key 缺失、无效或已撤销 | 重新授权或检查调用方配置 |
| 403 | DingTalk 身份不可用、未绑定、非成员、任务无权限 | 不重试绕过权限；检查 MissionOS 绑定和成员关系 |
| 404 | 任务不存在或权限隐藏资源 | 以 MissionOS 原有 403/404 语义处理 |
| 409 | 身份绑定歧义/冲突 | 修复 MissionOS 身份绑定数据 |
| 429 | 触发限流 | 按 `Retry-After` 退避 |
| 502 | 身份服务/MissionOS 响应错误或契约不兼容 | 查 `request_id` 和上游日志 |
| 503 | 配置缺失、secret 不一致、Redis/绑定存储不可用 | 先检查部署变量和服务依赖 |
| 504 | 上游超时 | 可有限重试；不要并发放大请求 |

网关、SDK 和 MissionOS 日志均不得记录 `X-Caller-Key`、签名密钥、access token 或完整请求体。建议只记录 `request_id`、内部 userId、目标工作区、HTTP 状态和耗时。

## 11. 配置变量

### 11.1 `newDingSDK`

| 变量 | 作用 | 配置要求 |
| --- | --- | --- |
| `MISSION_API_BASE_URL` | MissionOS 后端 HTTPS origin | 不带 `/api`、路径或查询参数；预发布指向预发布后端，正式指向正式后端 |
| `MISSION_WORKSPACE_ID` | 默认工作区 | 当前唯一工作区的 UUID；显式传其他工作区会被拒绝 |
| `MISSION_EXCHANGE_SECRET` | 服务间身份交换 HMAC 密钥 | 与 MissionOS 完全一致；至少 32 字符随机值；不能写入代码或文档 |
| `MISSION_CURSOR_SECRET` | 列表游标 HMAC 密钥 | 仅 newDingSDK 使用；至少 32 字符随机值；轮换会使旧游标失效 |
| `MISSION_REQUEST_TIMEOUT_MS` | 上游请求超时 | 建议 10000，范围 1000–30000 |

### 11.2 MissionOS

| 变量/依赖 | 作用 | 配置要求 |
| --- | --- | --- |
| `MISSION_EXCHANGE_SECRET` | 校验 newDingSDK 的服务间签名 | 与 newDingSDK 相同值；正式和预发布各自部署，但本次约定两边使用同一配置值 |
| `JWT_SECRET` | MissionOS 现有 JWT 签名 | 沿用已有安全值，不与 exchange secret 混用 |
| `REDIS_URL` | nonce 重放保护及现有会话依赖 | 所有副本共享、可用；不能使用进程内替代 |
| `dingtalk_notify_identities` | DingTalk ID 到 MissionOS 用户绑定 | 必须已有有效绑定；不自动创建或授权 |
| `PROJECT_OWNER_BYPASS_ENABLED` | MissionOS 既有 owner 行为 | 由 MissionOS 当前配置决定，本接口不额外覆盖 |

密钥应配置在部署平台的 secret variable 中。`deploy-inventory` 仅提交变量名/非敏感配置或由部署系统注入密钥；不要把真实值提交到 Git。

## 12. 部署和环境关系

当前环境：

- MissionOS 正式：`https://mission.lechun.cc`
- MissionOS 预发布：`https://mission-staging.lechun.cc`
- `newDingSDK` 正式入口：`https://scmapi.lechun.cc/lechun-node`

上线顺序：

1. 先部署包含身份交换和任务读 handler 的 MissionOS 版本。
2. 确认 MissionOS 的 `/internal/auth/dingtalk/exchange` 能从 `MISSION_API_BASE_URL` 到达 Go 后端。
3. 在 `newDingSDK` 配置 URL、工作区和两个 secret；确认两边 `MISSION_EXCHANGE_SECRET` 完全一致。
4. 再部署/启用 `newDingSDK` 任务路由。
5. 先用 owner、普通成员、无权限成员分别做受控联调，再考虑正式环境。

当前预发布 MissionOS 已包含任务读接口代码；正式 MissionOS 是否已部署对应 commit 需单独确认，不能仅凭域名可访问判断。

## 13. 测试矩阵

### 13.1 认证测试

- 缺少 `X-Caller-Key` → 401；
- 无效、过期、撤销的 API Key → 401；
- API Key 能解析 userId 但没有 DingTalk 绑定 → 403；
- DingTalk ID 绑定歧义 → 409；
- MissionOS 工作区不存在或用户不是成员 → 403/404；
- 时间戳过期、nonce 重放、签名错误 → 401/403；
- Redis 不可用 → 503；
- 交换 token 不能用于写接口、WebSocket、Cookie 会话续期。

### 13.2 权限测试

- owner 能看到 MissionOS 现有规则允许的全部任务；
- admin/member 只能看到各自项目/任务权限允许的任务；
- 无权任务不出现在列表中且不计数；
- 无权详情保持 MissionOS 原有 403/404；
- 移除成员后新请求立即拒绝；
- 同一用户跨工作区访问被拒绝；
- 修改 MissionOS 原有 ACL 后，两个接口不需要复制规则即可跟随变化。

### 13.3 数据和分页测试

- `limit` 边界、cursor 过期、cursor 篡改和跨用户复用；
- 保持筛选参数不变翻页，结果按 UUID 去重；
- 创建时间 `[from,to)` 边界；
- 完成、重新打开、再次完成以及活动异步延迟；
- `completed_at` 不错误使用 `due_date` 或 `updated_at`；
- 搜索、状态、项目、负责人、归档状态组合；
- 任务被删除/归档/权限变化时的 `best_effort` 行为。

## 14. 当前线上联调阻塞

已实测：

- `lechunapikey` 有效；
- API Key → userId 成功；
- userId → DingTalk ID 成功；
- `newDingSDK` 新接口代码已部署；
- 直接访问 `https://mission-staging.lechun.cc/internal/auth/dingtalk/exchange` 和正式域名均返回 Next.js HTML 404；
- 访问 `/api/internal/auth/dingtalk/exchange` 返回 Go 的 404，也不是可用替代路径；
- `newDingSDK` 因上游 404 将结果映射为 `TASK_NOT_FOUND`，因此当前错误不能说明任务不存在或用户无权限。

原因：

`multica/apps/web/proxy.ts` / `next.config.ts` 当前只代理 `/api`、`/auth`、`/uploads`、`/ws`、`/health`、`/v1` 等路径，没有代理 `/internal`。请求到达前端域名后被 Next.js 当作页面路由处理，尚未进入 Go 的身份交换 middleware/handler。

### 建议修复方式

方案 A（优先）：在 MissionOS 前端反向代理中加入：

```text
/internal/:path*  →  MissionOS Go backend /internal/:path*
```

重新部署 MissionOS 预发布后，用同一用户和工作区验证交换接口返回 200。

方案 B：如果部署平台提供独立 MissionOS backend origin，将 `newDingSDK` 的 `MISSION_API_BASE_URL` 改为该后端 origin，而不是前端域名。

路由打通前，无法可靠判断 owner、普通成员、任务是否存在、任务 ACL 或 secret 是否一致；不要依据当前 `TASK_NOT_FOUND` 做权限结论。

## 15. 安全边界和风险

主要风险及控制：

| 风险 | 控制 |
| --- | --- |
| API Key 泄露 | 只放 `X-Caller-Key`，网关和日志脱敏；撤销后每次请求重新校验 |
| DingTalk ID 伪造 | ID 只能由 lechun 身份服务解析，客户端不能传入可信身份 |
| 交换请求重放 | HMAC + 时间窗 + Redis nonce |
| Mission token 泄露 | 5 分钟短期、只读、workspace-bound、no-store；SDK 不返回、不持久化 |
| cursor 伪造/串用 | HMAC 绑定用户、DingTalk ID、工作区和完整查询参数 |
| 权限复制漂移 | 任务列表/详情最终调用 MissionOS 原有 handler |
| 跨工作区访问 | 默认工作区约束、令牌 claim 和 handler 三层校验 |
| 智能体提示注入 | 任务描述作为数据处理，不当作系统/工具指令 |
| 多副本状态不一致 | Redis 和签名密钥共享；不能依赖单进程内存完成重放保护 |

## 16. 上线前检查项与范围边界

### 上线前必须完成

1. 修复/确认 `/internal` 到 Go backend 的生产路由；
2. 确认正式 MissionOS 已部署兼容的身份交换和任务读版本；
3. 在两套服务配置完全一致的 `MISSION_EXCHANGE_SECRET`；
4. 确认 `MISSION_API_BASE_URL` 指向实际 MissionOS backend origin；
5. 确认 Redis、DingTalk 身份绑定和工作区成员数据可用；
6. 完成 owner/member/无权限/跨工作区/API Key 撤销/分页/完成时间测试；
7. 在网关和应用日志中检查密钥、token、完整请求体没有泄露；
8. 对生产部署建立回滚方案，并保存 `request_id` 便于定位。

### 不在本次范围

- 修改 MissionOS 原有权限规则；
- 自动创建 MissionOS 用户或工作区成员；
- 给用户生成、保存或管理 MissionOS PAT；
- 把任务接口扩展为写接口；
- 把 `completed_at` 升级为审计级不可变事件；
- 为所有 MissionOS 页面路由建立新的登录体系。

## 17. 相关文档

- `newDingSDK/docs/mission-tasks.md`：底层接口契约、参数和错误码。
- `newDingSDK/docs/mission-tasks-agent-guide.md`：面向智能体的调用与翻页指南。
- `multica/docs/engineering/mission-read-api.md`：MissionOS 服务间交换、权限和部署验证说明。
- `deploy-inventory/apps/lechun/backend/newdingsdk.yml`：部署变量清单（不包含 secret 实际值）。

