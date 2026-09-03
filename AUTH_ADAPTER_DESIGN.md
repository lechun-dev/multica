# MissionOS 登录适配器改造设计

> 状态：设计稿（未开始实现）
> 适用范围：MissionOS Web、桌面端及后端登录入口
> 目标：统一钉钉登录、邮箱验证码登录，并为企微、飞书、OIDC 等方式预留扩展点。

## 1. 背景与原则

当前系统有两条登录链路：

- 钉钉 OAuth：`/auth/dingtalk/start` → `/auth/dingtalk`，在
  `server/cmd/server/dingtalk_login.go` 和 `extensions/dingtalk-notify` 中实现；
- 邮箱验证码：`POST /auth/send-code` → `POST /auth/verify-code`，在
  `server/internal/handler/auth.go` 中实现。

两条链路最终都查找或创建 `user` 表用户，并通过现有 JWT 逻辑签发令牌。改造时保持路由和响应兼容，避免前端、桌面端和上游更新产生不必要的冲突。

核心原则：

1. 统一认证完成后的身份结果，不强行统一钉钉 OAuth 和邮箱验证码的交互协议。
2. “登录适配器”只负责证明外部身份；“账号绑定”负责映射到 MissionOS `user_id`；“会话服务”负责签发令牌。
3. 组织同步与登录解耦。登录成功不应因为部门接口暂时失败而失败。
4. 不删除现有钉钉通知数据结构，先通过兼容桥接迁移。
5. 项目和任务权限只消费统一的 MissionOS 用户身份，不直接依赖钉钉或邮箱字段。

## 2. 目标架构

```text
钉钉 OAuth ─┐
邮箱验证码 ─┼─> LoginProvider ─> ExternalIdentity
企微/飞书 ─┤                          │
OIDC       ┘                          ↓
                              IdentityLinker
                                      ↓
                               MissionOS user_id
                                      ↓
                               SessionService
                                      ↓
                                  JWT/Session
```

组织同步是另一条独立链路：

```text
DingTalkOrganizationProvider / WeComOrganizationProvider / ...
                                      ↓
                         组织、部门、成员关系
                                      ↓
                              项目权限计算
```

同一个用户可以使用多个登录方式，登录来源不应改变其项目权限。

## 3. 统一接口

建议新增独立包（建议位置：`server/internal/auth/`），不把第三方 SDK 细节放入通用服务。

### 3.1 认证结果

```go
type ExternalIdentity struct {
    Provider       string // dingtalk、email、wecom、feishu、oidc
    TenantID       string // 企业/租户标识；邮箱可为空
    ExternalUserID string // 外部系统稳定用户 ID
    UnionID        string
    Email          string
    Name           string
    AvatarURL      string
    EmailVerified  bool
}
```

第三方返回内容必须在适配器边界校验。`ExternalUserID` 是绑定依据，不能用姓名自动绑定。

### 3.2 OAuth 提供者

```go
type LoginProvider interface {
    Name() string
    Begin(ctx context.Context, req LoginRequest) (LoginRedirect, error)
    Complete(ctx context.Context, req LoginCallback) (ExternalIdentity, error)
}
```

钉钉实现保留现有 OAuth 换 token、查用户资料、解析 `ding_user_id/union_id/open_id` 的代码，仅在出口转换为 `ExternalIdentity`。

### 3.3 验证码提供者

邮箱不是 OAuth，单独定义验证码接口：

```go
type VerificationLoginProvider interface {
    SendCode(ctx context.Context, email string) error
    VerifyCode(ctx context.Context, email, code string) (ExternalIdentity, error)
}
```

现有 `/auth/send-code` 和 `/auth/verify-code` 路由继续保留，处理器内部改为调用 `EmailCodeProvider`。

### 3.4 账号绑定与会话

```go
type IdentityLinker interface {
    ResolveOrCreate(ctx context.Context, identity ExternalIdentity) (User, error)
}

type SessionService interface {
    Issue(ctx context.Context, userID string) (token string, err error)
}
```

钉钉和邮箱适配器都不得自行创建第二套用户或自行拼装 JWT。所有登录入口最终执行：

```text
ExternalIdentity
→ IdentityLinker.ResolveOrCreate
→ SessionService.Issue
→ 统一登录响应
```

## 4. 外部身份绑定表

建议新增独立表 `auth_identity_links`（名称可在实现阶段按现有迁移命名规范调整）：

| 字段 | 说明 |
| --- | --- |
| `id` | 本地主键 |
| `provider` | 登录提供者 |
| `tenant_id` | 企业/租户标识，可为空 |
| `external_user_id` | 外部系统稳定用户 ID |
| `union_id` | 外部系统可选的跨应用 ID |
| `user_id` | MissionOS 用户 ID |
| `email` | 登录时得到的邮箱快照 |
| `status` | active、disabled 等状态 |
| `last_login_at` | 最近登录时间 |
| `created_at/updated_at` | 审计时间 |

必须建立唯一约束：

```text
(provider, tenant_id, external_user_id)
```

绑定规则：

1. 一个外部身份只能绑定一个 MissionOS 用户。
2. 一个 MissionOS 用户可以绑定多个提供者。
3. 邮箱统一执行 trim、转小写和格式校验。
4. 邮箱自动匹配只能在已验证且符合企业可信域名策略时执行。
5. 发现冲突时返回明确错误，禁止静默合并账号。

`dingtalk_notify_identities` 目前还被钉钉通知功能读取，不能直接删除。迁移顺序应为：新表建表 → 回填已有钉钉绑定 → 登录优先读取新表 → 通知模块完成桥接 → 再评估旧表下线。

## 5. 邮箱登录/注册规则

邮箱验证码验证成功后返回：

```text
ExternalIdentity{Provider: "email", Email: normalizedEmail, EmailVerified: true}
```

`IdentityLinker` 继续遵守现有策略：

- 已存在用户：直接登录；
- 不存在用户：受注册开关和邮箱域名限制约束后创建；
- 临时禁用用户：拒绝登录；
- 新用户：保留现有 signup 事件；
- JWT 的 claims 和过期策略：继续由 `SessionService` 统一处理。

验证码本身仍应保留现有的限流、过期、单次使用和错误次数限制。

## 6. 钉钉组织同步

钉钉登录返回的部门资料只用于“当前用户快速同步”，完整组织树和全员成员同步由独立的 `DingTalkOrganizationProvider` 定时或手动执行。

同步失败处理：

- OAuth 身份有效且用户可以登录；
- 使用最近一次成功同步的组织关系计算权限；
- 记录同步失败并重试；
- 不在登录处理器中直接计算项目权限。

以后接入企微、飞书或其他 OA 时，只需增加对应的登录适配器和组织适配器，不修改账号绑定和项目权限核心逻辑。

## 7. 兼容改造步骤

1. 增加 `ExternalIdentity`、`LoginProvider`、`IdentityLinker` 和 `SessionService` 接口及测试。
2. 用 `DingTalkLoginProvider` 包装现有 `DingTalkOAuthProvider`，保留钉钉路由。
3. 用 `EmailCodeProvider` 包装现有验证码流程，保留邮箱路由和响应结构。
4. 增加身份绑定表和一次性回填；旧钉钉表保留兼容读取。
5. 将组织同步从登录处理器中抽离，并把同步结果写入通用组织目录。
6. 全部入口切换到统一 `IdentityLinker` 和 `SessionService` 后，再逐步删除处理器中的重复逻辑。

## 8. 验收标准

- 钉钉登录和邮箱登录都能得到同一用户的统一 JWT 响应。
- 同一用户绑定钉钉和邮箱后，登录来源切换不改变项目/任务权限。
- 同一钉钉企业中的外部身份不会重复创建 MissionOS 用户。
- 组织同步短暂失败不阻塞已经完成认证的登录。
- 现有 `/auth/dingtalk/*`、`/auth/send-code`、`/auth/verify-code` 路由保持兼容。
- 绑定冲突、禁用用户、未验证邮箱和越权组织切换均有明确错误码和审计日志。
