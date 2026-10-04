# Claude Messages 会话永久绑定

默认关闭。关闭时，Claude Messages 的选号、粘性会话和故障转移保持官方行为，已有绑定数据也不会被删除。

第一阶段只接入 `POST /v1/messages` 里非 Gemini 分组的路径。OpenAI、Gemini、count_tokens 以及其他供应商接口不会读写这张绑定表。

## 行为

会话第一次被官方调度分到某个订阅账号后，绑定会先写入数据库，然后才向上游发请求。之后这个会话只检查原账号：

- 原账号仍满足额度、RPM、并发、模型支持和会话容量时，继续用它。
- 原账号限流、额度不足、停用、授权失效、模型不支持、移出分组、容量不足或已被删除时，不改选其他订阅账号。
- 可安全重试的上游故障只在原账号上有界重试。`same_account_retry_limit` 为 `-1` 时沿用账号的 `pool_mode_retry_count`；大于 0 时再收紧；`0` 连同带截止时间的同账号重试一起禁用。
- 原账号暂时恢复后，后续请求回到原账号。转到兜底分组或第三方都不会修改、也不会新建主绑定。
- 流式响应已经开始写出后中断：直接结束，不拼接另一个回退目标，也不重放。判断包含 SSE 已开始、响应已写出，以及写出量相对请求入口基线的变化。
- 客户端取消、参数错误、prompt 过长不会进入兜底分组或第三方。
- `run_mode: simple` 下官方选号不按分组过滤。严格模式同样不会因为账号不在当前 Key 分组而把绑定永久判成 `removed_from_pool`。标准模式仍会按分组判断。
- 平台过滤与官方 `isAccountAllowedForPlatform` 一致：未强制平台时，Anthropic/Gemini 可以选中开启混合调度的 Antigravity 账号；强制平台时只允许同一平台。
- 严格模式在选号成功之后、请求失败提前返回时，会释放这次已经登记的会话槽。

原账号不能承接、且响应还没写出时，按 `fallback_order` 尝试已配置的回退目标：

- `group_first`（默认）：先把请求交给 `fallback_group_id` 指向的 Sub2API 分组，由该分组自己的调度、模型映射和计费处理。这个目标失败且响应仍未写出时，再尝试第三方。
- `third_party_first`：顺序相反。
- 只配置了一个目标时，另一个会被跳过。两个都没配置，或都失败，返回 `strict_session_fallback_required`。最后一个失败的目标是第三方、并且没有剩余目标时，返回 `strict_session_third_party_failed`。
- 兜底分组不能等于本次请求的原分组。保存配置时还不能知道每把 Key 的分组，所以相等检查发生在请求路径：相等就跳过该目标，也不会回到原分组的订阅账号池。保存时会拒绝不存在、未启用，或平台不是 Anthropic / Antigravity 的分组。
- 兜底分组内使用官方 1 小时粘性，键是兜底分组 ID 加绑定键。这不是主绑定。原账号恢复后，主路径不会读取这条粘性。分组内部可以按官方规则在该分组的账号之间切换，不能选出原分组的账号。simple 模式平时忽略分组；兜底转发仍强制只看该分组。
- 第三方成功不记 Sub2API 用量、不扣余额。兜底分组按该分组自己的计费规则记账。

外层 New API 仍可以把 `strict_session_fallback_required` 转到它自己的渠道。外层不得再把请求送回同一个订阅账号池。

可识别错误码：

| code | 含义 |
| --- | --- |
| `strict_session_id_required` | 严格模式缺少稳定会话 ID |
| `strict_session_store_unavailable` | 绑定存储读写失败，没有重新选号 |
| `strict_session_fallback_required` | 两个回退目标都没配置，或都失败，且最后没有写出第三方响应 |
| `strict_session_third_party_failed` | 第三方失败，且没有剩余的兜底分组可以再试 |
| `strict_session_stream_interrupted` | 流已经输出，禁止拼接和重放 |

响应尚未写出时返回 `X-Sub2API-Error-Code`。内部账号 ID 只出现在服务端日志里，不通过响应头返回给客户端。

## 会话身份

永久键只接受稳定会话 ID，按下面顺序取值：

1. `metadata.user_id` 里的 Claude Code `session_id`
2. `X-Claude-Code-Session-Id`
3. `session_header`（默认 `X-Session-Id`）

带 `cache_control` 的内容哈希、消息摘要都不会成为永久会话 ID。没有上述 ID 时，严格模式直接拒绝。

绑定键为 `SHA-256("v2|session=" + session_id)`。固定版本前缀之外只包含客户端原始会话 ID；API Key、用户、设备、协议、分组和模型都不参与身份计算。

同一个会话 ID 在全系统命中同一条绑定，即使更换 API Key、设备或请求入口。不同会话 ID 独立分配账号。系统不识别会话属于哪个人，不需要终端用户请求头；客户端主动复用相同 ID，也会被视为同一会话。

JSON 格式的 `metadata.user_id` 只需提供非空字符串 `session_id`，不要求 `device_id` 或 `account_uuid`。旧版 `user_<device>_account_<account>_session_<id>` 格式仍支持。此处只改变严格绑定的会话提取，不放宽上游设备指纹解析规则。各来源均拒绝控制字符、非法 UTF-8 和超过 255 字符的 ID；缺失时不生成随机 ID，也不用内容哈希代替。

API Key 仍按现有流程鉴权、计费；绑定账号每次按当前请求的分组、模型和账号状态检查。标准模式下，另一把 Key 无权使用原账号所在分组时走已配置的回退目标，不凭会话 ID 越过分组限制，也不改写主绑定。simple 模式继续遵循官方忽略分组的规则。

数据库保存绑定键、短指纹、账号 ID、首次请求的协议、API Key ID 和分组；后三者只作审计信息。不再读取或写入终端用户指纹，不保存正文、Token、Cookie 或完整会话 ID。

### 旧草稿绑定兼容性

本次不修改已经存在的 `242_strict_session_bindings.sql`，避免破坏迁移校验。表里的 `end_user_fingerprint` 作为历史可空列保留，新代码不使用它；该迁移中描述旧绑定键的注释属于旧草稿语义，以本节为准。

旧草稿使用 API Key / 终端用户参与计算的 v1 绑定键，新版只读写会话级 v2 键，两者不自动合并。由于表中没有完整会话 ID，不能从旧哈希可靠恢复新键。如果已经试运行旧草稿并积累绑定，上线前必须单独准备、核验会话映射和冲突处理方案；直接切换会使旧会话首次请求建立新的 v2 绑定。旧行不会被删除。

## 和官方粘性会话的关系

在基线分支 `asterflow`（上游 0.2.13）上，官方粘性会话仍然是 `stickySessionTTL = 1h` 的缓存。账号不可调度或模型限流时会清理这条缓存并进入其他账号。上游失败会把账号放入排除列表后重选。`gateway.max_account_switches=0` 不会禁用切换：初始化只接受大于 0 的值，否则保留默认上限 10。这些行为在严格模式关闭时保持不变。

启用严格模式后，数据库绑定才是事实来源。Redis 键 `strict_session_binding:` 只做加速，TTL 为 6 小时。缓存过期、清空、Redis 故障或进程重启都回源数据库，不会让绑定本身过期。Redis 读失败不会被当成未绑定；数据库读失败或写失败会返回 `strict_session_store_unavailable`，不会先用一个还没落库的账号。

并发的第一次请求用 `INSERT ... ON CONFLICT (binding_key) DO UPDATE SET account_id = strict_session_bindings.account_id RETURNING ...`。空更新不改写赢家的账号，但会等冲突事务提交并返回已落库的那一行。输家释放自己占的会话槽，再按赢家的账号继续，不会返回 503。`DO NOTHING` 加同语句查询会读到插入前的快照，因此不采用。

启用前已经过期的官方粘性缓存无法恢复。若缓存里还留着同一稳定会话 ID 的官方粘性记录，首次分配会把它当作选号线索；这不是历史保证。

## 配置

```yaml
gateway:
  strict_session_binding:
    enabled: false
    session_header: "X-Session-Id"
    same_account_retry_limit: -1
    # group_first 或 third_party_first。默认先兜底分组。
    fallback_order: group_first
    # 0 表示不启用。不能在请求时等于这把 Key 的原分组。
    fallback_group_id: 0
    third_party:
      enabled: false
      base_url: ""
      api_key: ""
      timeout_seconds: 0
```

环境变量与配置键对应，例如 `GATEWAY_STRICT_SESSION_BINDING_ENABLED=true`、`GATEWAY_STRICT_SESSION_BINDING_FALLBACK_ORDER=group_first`、`GATEWAY_STRICT_SESSION_BINDING_FALLBACK_GROUP_ID=20`、`GATEWAY_STRICT_SESSION_BINDING_THIRD_PARTY_API_KEY`。第三方地址必须是绝对 `http` 或 `https` URL，不能把凭据写进 URL，也不能跟随重定向。`base_url` 是 API 根，例如 `https://relay.example.com`；程序会请求 `{base}/v1/messages`。

## 管理后台

位置：管理后台 → 系统设置 → 网关服务，页内第一张卡片「严格会话绑定」。

可配置：启用开关、回退顺序、兜底分组（下拉只列出启用中的 Anthropic / Antigravity 分组）、同账号重试、会话头、第三方地址 / 密钥 / 超时。密钥按现有敏感字段处理：接口只返回 `strict_session_third_party_api_key_configured`，不回显明文；保存时留空表示保留已有密钥。

优先级：

- 管理员还没在页面上保存过这些字段时，生效的是进程启动时的 yaml / 环境变量。
- 页面一旦保存，数据库成为事实来源，下一次请求立即使用，不需要重启。请求热路径有 30 秒缓存，保存时会立刻写入这份缓存。
- 保存之后再改 yaml 或环境变量，不会盖过数据库，直到再次在页面上保存。
- 旧客户端保存设置时如果省略这些字段，不会把已保存的严格会话配置清掉。

页面上的分组校验不能预先知道每把 Key 的原分组，所以「兜底分组等于原分组」仍在请求时跳过。

`timeout_seconds` 同时作为响应头超时和流空闲超时，不是整段响应体的总时长。`0` 使用默认值：响应头 60 秒，两次读取之间空闲 5 分钟。正在输出的长流不会被总时长截断，停住的流也不会无限挂起。第三方 4xx 按状态码和响应体原样返回；5xx 仍是 `strict_session_third_party_failed`，不会再拼接订阅池的响应。流式转发会按块 Flush。

原账号槽位占满时，只按 `gateway.scheduling.sticky_session_max_waiting` 和 `sticky_session_wait_timeout` 在原账号上等待。等不到就进入回退目标，或返回明确错误。

进程内第三方成功时，本阶段不把用量记到订阅账号上，也不从 Sub2API 余额扣费。需要由 Sub2API 计费时，请关闭 `third_party.enabled`，让外层 New API 接收 `strict_session_fallback_required` 后走它自己的渠道和账单。

## 迁移、启用与回滚

迁移文件：`backend/migrations/242_strict_session_bindings.sql`。它只创建 `strict_session_bindings`。`binding_key` 是 `VARCHAR(64)`，避免 `CHAR` 补空格。`account_id` 没有外键，删除账号不会级联删除绑定，因此「从未分配」和「原账号已不存在」可以区分。

启用：

1. 先备份数据库。永久指应用不会自动过期，不表示可以不备份。
2. 发布包含该迁移的版本，确认 `strict_session_bindings` 已创建。
3. 将 `gateway.strict_session_binding.enabled` 设为 `true` 后重启。
4. 如需进程内中转，再打开 `third_party` 并提供地址和密钥。否则保持关闭，由外层识别错误码。

回滚：

1. 把 `enabled` 设为 `false` 并重启。官方调度立即恢复，绑定表保留。
2. 回退到没有这段代码的旧版本同样不会删除绑定表。旧版本会忽略这张表。
3. 不要为了关闭功能而 `DROP TABLE`。只有确认不再需要这些会话关系时，才由运维另行归档或删除。

## 日志

`strict_session.dispatch` 以及 handler 的 `strict_session.*` 只记录原账号 ID、会话短指纹、回退原因和最终链路（`origin`、`origin_check`、`third_party`、`error`、`stream_interrupted`）。严格模式开启后，`GenerateSessionHash` 和粘性调试日志同样只记录短指纹或是否存在设备 ID，不记录正文、Token、Cookie、完整 `session_id`、`device_id` 或 `metadata.user_id`。功能关闭时这些官方日志保持原样。

## 审查修正

- 会话身份只由客户端会话 ID 决定；更换 API Key 不创建新绑定。管理页不再提供终端用户设置。
- 流已经开始写出后，所有严格回退调用点都传入请求入口时的写出量，并且 `streamStarted` 本身也会阻止拼接第三方响应。
- 并发首写输家读回赢家的账号，不再因为冲突返回 503。
- `run_mode: simple` 不按分组把绑定判成永久移出。
- 严格路径的会话哈希和粘性日志不再输出完整 `session_id`、`device_id` 或 `metadata.user_id`。
- Redis 绑定缓存 TTL 为 6 小时，数据库仍是唯一事实来源。
- 第三方客户端拒绝重定向，流式响应按块 Flush；`timeout_seconds` 只限制响应头和读空闲，不截断仍在输出的长流。4xx 原样透传，5xx 仍返回明确错误。
- 不再通过 `X-Sub2API-Bound-Account-Id` 返回内部账号 ID。
- `same_account_retry_limit=0` 禁用同账号重试；默认值改为 `-1`，表示沿用账号配置。

## 未覆盖范围

- Gemini 分组走 `/v1/messages` 的原有循环。
- OpenAI / Responses / Chat Completions / 图片 / 其他供应商。
- `POST /v1/messages/count_tokens` 不建立绑定。
- 启用前已过期的官方粘性会话不能追溯恢复。
- 进程内第三方的工具调用、上下文窗口和价格由该中转自己决定；不兼容的请求不会在失败后再回到订阅池。
