# Claude Messages 会话永久绑定

默认开启，仅在管理后台保存到数据库的配置中调整。关闭后恢复普通调度，正在使用的会话可能切换账号；绑定数据保留，重新开启后沿用原绑定。

第一阶段只接入 `POST /v1/messages` 里非 Gemini 分组的路径。OpenAI、Gemini、count_tokens 以及其他供应商接口不会读写这张绑定表。

## 行为

会话第一次被官方调度分到某个订阅账号后，绑定会先写入数据库，然后才向上游发请求。之后这个会话只检查原账号：

- 原账号仍满足额度、RPM、并发、模型支持、渠道模型限制、利润控制和会话容量时，继续用它。
- 客户端是否允许访问当前分组在首次选号和已有绑定复用前统一检查。非 Claude Code 客户端访问 `claude_code_only` 分组时直接拒绝，即使配置了 fallback group 也不会在严格模式内换组；强制平台入口保留普通调度的客户端限制例外。
- 当前分组要求 `require_privacy_set` 时，原账号也必须满足隐私设置要求。Antigravity 模型限流复用完整的可调度判断：已开启 `allow_overages` 且未标记积分耗尽时仍可使用原账号。
- 原账号限流、额度不足、停用、授权失效、模型不支持、移出分组、容量不足或已被删除时，不改选其他订阅账号。
- 可安全重试的上游故障只在原账号上有界重试。`same_account_retry_limit` 为 `-1` 时沿用账号的 `pool_mode_retry_count`；大于 0 时再收紧；`0` 连同带截止时间的同账号重试一起禁用。
- 原账号恢复后，后续请求继续使用原账号；拒绝请求不会改写主绑定。
- 流式响应已经开始写出后中断：直接结束，不拼接另一个回退目标，也不重放。判断包含 SSE 已开始、响应已写出，以及写出量相对请求入口基线的变化。
- 客户端取消、参数错误、prompt 过长不会进入兜底分组或第三方。
- `run_mode: simple` 下官方选号不按分组过滤。严格模式同样不会因为账号不在当前 Key 分组而把绑定永久判成 `removed_from_pool`。标准模式仍会按分组判断。
- 平台过滤与官方 `isAccountAllowedForPlatform` 一致：未强制平台时，Anthropic/Gemini 可以选中开启混合调度的 Antigravity 账号；强制平台时只允许同一平台。
- 严格 Messages 失败或重试时，只释放本请求的并发槽和等待计数；共享会话容量登记由原有空闲超时回收。

原账号不能承接且响应尚未写出时，Sub2API 直接返回 **HTTP 503**，不在进程内切换分组、不转第三方，也不改写绑定。具体选择哪个备用分组或渠道，由上层 AsterFlow（New API）决定。

例如渠道只允许 Sonnet，但已绑定账号本身也支持 Opus：同一会话请求 Opus 时仍须执行渠道限制，返回：

```http
HTTP/1.1 503 Service Unavailable
X-Sub2API-Error-Code: strict_session_account_unavailable
Content-Type: application/json
```

```json
{
  "type": "error",
  "error": {
    "type": "session_binding_error",
    "code": "strict_session_account_unavailable",
    "reason": "channel_model_restricted",
    "message": "Strict session binding blocked subscription-account reassignment (channel_model_restricted)"
  }
}
```

上层应识别 `error.code`（响应头携带相同值），通过 `error.reason` 判断具体原因，不解析人类可读的 `message`。本改动只定义 Sub2API 出口；New API 的备用渠道策略需在上层单独配置或实现。仅将同一会话换一把 Sub2API Key，不会解除全局绑定；若备用 Key 的分组不允许原账号，仍返回错误。上层需要选择真正能承接的目标，避免循环重试同一个绑定。

可识别错误码：

| HTTP / code | 含义 |
| --- | --- |
| 400 / `strict_session_id_required` | 缺少合法稳定会话 ID |
| 503 / `strict_session_config_unavailable` | 配置读取失败且没有最近一次可信配置，未调用上游 |
| 503 / `strict_session_store_unavailable` | 绑定存储读写失败，没有重新选号 |
| 503 / `strict_session_account_unavailable` | 原账号不可服务，或在原账号上的安全重试已耗尽 |
| 流内 / `strict_session_stream_interrupted` | 响应已经输出，以错误事件结束，不拼接和重放；无法再改 HTTP 状态 |

`strict_session_account_unavailable` 的常见 `reason`：

| reason | 含义 |
| --- | --- |
| `claude_code_only` | 当前客户端不允许访问该分组；首次分配和已有绑定均拒绝，不进入 fallback group |
| `privacy_not_set` | 当前分组要求隐私设置，但原账号未满足；不会改选其他账号 |
| `channel_model_restricted` | 渠道定价模型列表不允许；按 requested、channel_mapped、upstream 或 response 的调度规则检查 |
| `profit_control` | 原账号不符合当前请求的利润控制要求，包括倍率缺失、非法或超过阈值；不改写绑定，恢复后继续使用原账号 |
| `model_unsupported` | 原账号不支持请求模型 |
| `disabled` / `account_deleted` | 原账号停用、调度关闭或已删除 |
| `removed_from_pool` / `platform_mismatch` | 当前请求分组或平台不允许原账号 |
| `authorization_invalid` | 原账号授权过期且自动暂停已启用 |
| `rate_limited` / `overloaded` / `temporarily_unschedulable` | 原账号限流、过载或临时不可调度 |
| `quota_exceeded` / `window_cost_exhausted` / `rpm_exceeded` | 额度、窗口费用或 RPM 超限 |
| `concurrency_exhausted` / `session_capacity` | 原账号并发等待耗尽或会话容量不足 |
| `scheduling_threshold` / `unschedulable` | 账号调度检查不通过 |
| `upstream_exhausted` | 上游或凭据故障，原账号的安全重试已耗尽 |
| `upstream_failed` | 转发失败且尚未向客户端传达错误 |

请求参数错误和 prompt 过长保留原错误，不包装成账号容量问题。首次未绑定且没有候选账号，仍返回普通调度错误；不会凭空创建绑定。内部账号 ID 只出现在服务端日志里，不通过响应头或错误体返回。

## 会话身份

永久键只接受稳定会话 ID，按下面顺序取值：

1. `metadata.user_id` 里的 Claude Code `session_id`
2. `X-Claude-Code-Session-Id`
3. `session_header`（默认 `X-Session-Id`）

带 `cache_control` 的内容哈希、消息摘要都不会成为永久会话 ID。没有上述 ID 时，严格模式直接拒绝。

绑定键为 `SHA-256("v2|session=" + session_id)`。固定版本前缀之外只包含客户端原始会话 ID；API Key、用户、设备、协议、分组和模型都不参与身份计算。

同一个会话 ID 在全系统命中同一条绑定，即使更换 API Key、设备或请求入口。不同会话 ID 独立分配账号。系统不识别会话属于哪个人，不需要终端用户请求头；客户端主动复用相同 ID，也会被视为同一会话。

JSON 格式的 `metadata.user_id` 只需提供非空字符串 `session_id`，不要求 `device_id` 或 `account_uuid`。旧版 `user_<device>_account_<account>_session_<id>` 格式仍支持。此处只改变严格绑定的会话提取，不放宽上游设备指纹解析规则。各来源均拒绝控制字符、非法 UTF-8 和超过 255 字符的 ID；缺失时不生成随机 ID，也不用内容哈希代替。

API Key 仍按现有流程鉴权、计费；绑定账号每次按当前请求的分组、模型和账号状态检查。标准模式下，另一把 Key 无权使用原账号所在分组时直接返回 `strict_session_account_unavailable`，不凭会话 ID 越过分组限制，也不改写主绑定。simple 模式继续遵循官方忽略分组的规则。

数据库保存绑定键、短指纹、账号 ID、首次请求的协议、API Key ID 和分组；后三者只作审计信息。不再读取或写入终端用户指纹，不保存正文、Token、Cookie 或完整会话 ID。

### 旧草稿绑定兼容性

本次不修改已经存在的 `242_strict_session_bindings.sql`，避免破坏迁移校验。表里的 `end_user_fingerprint` 作为历史可空列保留，新代码不使用它；该迁移中描述旧绑定键的注释属于旧草稿语义，以本节为准。

旧草稿使用 API Key / 终端用户参与计算的 v1 绑定键，新版只读写会话级 v2 键，两者不自动合并。由于表中没有完整会话 ID，不能从旧哈希可靠恢复新键。如果已经试运行旧草稿并积累绑定，上线前必须单独准备、核验会话映射和冲突处理方案；直接切换会使旧会话首次请求建立新的 v2 绑定。旧行不会被删除。

## 和官方粘性会话的关系

在基线分支 `asterflow`（上游 0.2.13）上，官方粘性会话仍然是 `stickySessionTTL = 1h` 的缓存。账号不可调度或模型限流时会清理这条缓存并进入其他账号。上游失败会把账号放入排除列表后重选。`gateway.max_account_switches=0` 不会禁用切换：初始化只接受大于 0 的值，否则保留默认上限 10。这些行为在严格模式关闭时保持不变。

启用严格模式后，数据库绑定才是事实来源。Redis 键 `strict_session_binding:` 只做加速，TTL 为 6 小时。缓存过期、清空、Redis 故障或进程重启都回源数据库，不会让绑定本身过期。Redis 读失败不会被当成未绑定；数据库读失败或写失败会返回 `strict_session_store_unavailable`，不会先用一个还没落库的账号。

并发的第一次请求用 `INSERT ... ON CONFLICT (binding_key) DO UPDATE SET account_id = strict_session_bindings.account_id RETURNING ...`。空更新不改写赢家的账号，但会等冲突事务提交并返回已落库的那一行。输家释放本请求的并发槽，保留共享会话容量登记，再检查赢家的账号；冲突本身不会导致 503，但赢家不符合当前请求的准入条件（例如利润控制）时仍会返回 503。`DO NOTHING` 加同语句查询会读到插入前的快照，因此不采用。

利润控制复用普通调度的请求计价上下文和判定逻辑。在登记会话或占用并发槽之前检查原账号，并把生效的利润检查上下文随选号结果传给现有抢槽后终检；排队期间账号倍率发生变化时，终检拒绝请求并释放本请求的并发槽和等待计数。未启用利润控制时沿用原行为，普通选号、重选和回退逻辑不因本修正而改变。

准入判断与选号、资源操作分开：普通调度和严格绑定共用[客户端及隐私检查](../backend/internal/service/gateway_admission.go)，模型准入复用 `IsSchedulableForModelWithContext`。共享判断不换组、不写绑定、不占槽；普通调度在不合格候选之外继续选择，严格绑定直接拒绝。调度阈值同步、会话登记与抢槽留在调度流程中，已有绑定和并发首写赢家使用同一个检查入口。已有会话仍使用窗口费用和 RPM 的粘性预留规则，抢槽后的利润终检保留。

严格 Messages 的会话容量登记可被同一会话的并发请求共享，单个失败请求没有注销整个登记的所有权。因此存储失败、并发首写落败、排队拒绝、利润终检拒绝和转发失败均只释放本请求的并发槽/等待计数，容量登记由账号原有的空闲超时回收；失败的新会话也可能占用容量直到该超时。这样可避免严格 Messages 自身的失败、竞争和重试删掉其他请求共享的登记。普通模式和 `count_tokens` 的原有注销行为未改，空闲超时也不是在途请求的引用计数。

启用前已经过期的官方粘性缓存无法恢复。若缓存里还留着同一稳定会话 ID 的官方粘性记录，首次分配会把它当作选号线索；这不是历史保证。

## 配置

此功能不再读取 YAML 或 `GATEWAY_STRICT_SESSION_BINDING_*` 环境变量，也没有启动配置与管理页配置的优先级。旧配置文件中的相关条目应删除。旧草稿中的 `strict_session_fallback_*` 和 `strict_session_third_party_*` 数据库设置不再读取，也不在管理接口返回或接受保存；历史值保留用于回滚，不会启用内部回退。

代码默认值：开启严格绑定、会话头 `X-Session-Id`、同账号重试 `-1`。只有数据库读取成功且相应键不存在时才采用默认值；已保存的 `false` 不会被默认值覆盖。

## 管理后台

位置：管理后台 → 系统设置 → 网关服务，页内第一张卡片「严格会话绑定」。

可配置：启用开关、同账号重试上限、附加会话头。内部兜底分组、回退顺序和第三方地址 / 密钥 / 超时控件已移除。

配置生效与故障行为：

- 保存前校验，数据库写入成功后立即更新当前实例缓存；写入失败保持原缓存不变。旧客户端省略这些字段时不修改严格会话配置。
- 每个请求开始时取一份完整配置，身份解析与同账号重试均沿用这份快照；保存设置影响后续请求。
- 各实例缓存有效期 30 秒。其他实例最多在下一次缓存过期读取后生效，无需重启；数据库故障时可能延迟更久。
- 刷新失败时保留最近一次成功读取的整份配置（包括开关、会话头和重试上限），记录错误，并在 5 秒后允许重试，不会临时套用部分默认值。
- 冷启动没有可信配置且数据库读取失败时，相关 Messages 请求返回 503 `strict_session_config_unavailable`，不调用上游；管理路由不经过这个拦截，可以在依赖恢复后继续调整配置。
- 缓存刷新与本实例保存串行，旧的刷新结果不会覆盖刚刚提交的新配置。其他实例仍使用上述 30 秒刷新规则。

原账号槽位占满时，只按 `gateway.scheduling.sticky_session_max_waiting` 和 `sticky_session_wait_timeout` 在原账号上等待。等不到就返回明确错误，不换号或换组。

## 迁移、启用与回滚

迁移文件：`backend/migrations/242_strict_session_bindings.sql`。它只创建 `strict_session_bindings`。`binding_key` 是 `VARCHAR(64)`，避免 `CHAR` 补空格。`account_id` 没有外键，删除账号不会级联删除绑定，因此「从未分配」和「原账号已不存在」可以区分。

新增迁移 `243_strict_session_settings_database_only.sql` 只清理旧版 `override=false` 自动种下的无效默认配置，并删除旧来源标记；`override=true` 的管理员配置（包括关闭状态）和没有旧标记的显式配置均保留。迁移不修改绑定表，可重复执行。仅在旧 YAML 中配置过的值不会自动导入数据库。

启用：

1. 先备份数据库。永久指应用不会自动过期，不表示可以不备份。
2. 发布包含该迁移的版本，确认 `strict_session_bindings` 已创建。
3. 检查管理页配置。未保存过配置的新安装默认开启，缺少会话 ID 的 Messages 请求会返回 400。
4. 在上层 AsterFlow / New API 配置错误码识别和备用路由。Sub2API 内部不再配置回退目标。

回滚：

1. 在管理页关闭开关并保存。当前实例后续请求恢复普通调度，其他实例等待缓存刷新；绑定表保留。
2. 回退到没有这段代码的旧版本同样不会删除绑定表。旧版本会忽略这张表。
3. 不要为了关闭功能而 `DROP TABLE`。只有确认不再需要这些会话关系时，才由运维另行归档或删除。

## 日志

`strict_session.dispatch` 以及 handler 的 `strict_session.*` 只记录原账号 ID、会话短指纹、拒绝原因和最终链路（`origin`、`origin_check`、`error`、`stream_interrupted`）。严格模式开启后，`GenerateSessionHash` 和粘性调试日志同样只记录短指纹或是否存在设备 ID，不记录正文、Token、Cookie、完整 `session_id`、`device_id` 或 `metadata.user_id`。Messages 请求明确关闭此功能时仍沿用普通粘性日志；没有请求配置快照的共享哈希入口默认脱敏，不额外读取配置。

## 审查修正

- 已绑定会话执行渠道模型限制，受限时返回 `strict_session_account_unavailable` / `channel_model_restricted`。
- 已绑定会话与并发首写赢家执行利润控制，并保留抢槽后终检；受限时返回 `strict_session_account_unavailable` / `profit_control`。
- 删除严格绑定的进程内换组、第三方转发与后台配置。对应的 simple 兜底缓存范围和跨组映射问题随路径移除而消除。
- 会话身份只由客户端会话 ID 决定；更换 API Key 不创建新绑定。管理页不再提供终端用户设置。
- 流已经开始写出后，通过请求入口的写出量和 `streamStarted` 检测中断，不重放。
- 并发首写输家读回赢家的账号，不再因为冲突返回 503。
- `run_mode: simple` 不按分组把绑定判成永久移出。
- 严格路径的会话哈希和粘性日志不再输出完整 `session_id`、`device_id` 或 `metadata.user_id`。
- Redis 绑定缓存 TTL 为 6 小时，数据库仍是唯一事实来源。
- 不再通过 `X-Sub2API-Bound-Account-Id` 返回内部账号 ID。
- `same_account_retry_limit=0` 禁用同账号重试；默认值改为 `-1`，表示沿用账号配置。

## 未覆盖范围

- Gemini 分组走 `/v1/messages` 的原有循环。
- OpenAI / Responses / Chat Completions / 图片 / 其他供应商。
- `POST /v1/messages/count_tokens` 不建立绑定。
- 启用前已过期的官方粘性会话不能追溯恢复。
