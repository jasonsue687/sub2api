# Claude Messages 会话永久绑定

默认关闭。关闭时，Claude Messages 的选号、粘性会话和故障转移保持官方行为，已有绑定数据也不会被删除。

第一阶段只接入 `POST /v1/messages` 里非 Gemini 分组的路径。OpenAI、Gemini、count_tokens 以及其他供应商接口不会读写这张绑定表。

## 行为

会话第一次被官方调度分到某个订阅账号后，绑定会先写入数据库，然后才向上游发请求。之后这个会话只检查原账号：

- 原账号仍满足额度、RPM、并发、模型支持和会话容量时，继续用它。
- 原账号限流、额度不足、停用、授权失效、模型不支持、移出分组、容量不足或已被删除时，不改选其他订阅账号。
- 可安全重试的上游故障只在原账号上有界重试。重试次数沿用账号的 `pool_mode_retry_count`，也可用 `same_account_retry_limit` 再收紧。
- 原账号暂时恢复后，后续请求回到原账号。第三方成功或失败都不会改写绑定。
- 流式响应已经输出内容后中断：直接结束，不拼接第三方响应，也不重放。
- 客户端取消、参数错误、prompt 过长不会自动转第三方，也不会切到兜底分组。

原账号不能承接时：

- 配置了 `third_party`：请求发到这个独立中转，不进入订阅账号池。
- 未配置，或第三方也失败：返回 `session_binding_error`，供外层 New API 把该错误转到它自己的指定第三方渠道。外层不得再把请求送回同一个订阅账号池。

可识别错误码：

| code | 含义 |
| --- | --- |
| `strict_session_id_required` | 严格模式缺少稳定会话 ID |
| `strict_session_end_user_required` | 配置了终端用户头但请求没带 |
| `strict_session_store_unavailable` | 绑定存储读写失败，没有重新选号 |
| `strict_session_fallback_required` | 原账号不能承接，需要外层转到指定第三方 |
| `strict_session_third_party_failed` | 进程内第三方失败，没有回到订阅池 |
| `strict_session_stream_interrupted` | 流已经输出，禁止拼接和重放 |

响应头 `X-Sub2API-Error-Code` 与 `X-Sub2API-Bound-Account-Id` 在响应尚未写出时一并返回。

## 会话身份

永久键只接受稳定会话 ID，按下面顺序取值：

1. `metadata.user_id` 里的 Claude Code `session_id`
2. `X-Claude-Code-Session-Id`
3. `session_header`（默认 `X-Session-Id`）

带 `cache_control` 的内容哈希、消息摘要都不会成为永久会话 ID。没有上述 ID 时，严格模式直接拒绝。

绑定键是 SHA-256，包含：

- API Key ID（可信租户）
- 入站协议，固定为 `anthropic`。混合调度选中 Antigravity 账号也不会把同一会话拆成新绑定。
- 可选终端用户。只有配置了 `end_user_header` 才纳入，并且该头必填。信任来自「这把 Sub2API Key 只发给外层网关，外层完成自己的鉴权后再写这个头」。
- 稳定会话 ID

不会把分组 ID 或模型放进绑定键。换模型、换分组路由都不能绕过原绑定；原账号不在当前分组或不支持该模型时走第三方，而不是另选订阅账号。不同 API Key 即使提交相同会话 ID 也不会串绑。共用一把 Key 的多个终端用户必须配置 `end_user_header`。

数据库只保存绑定键、短指纹、账号 ID、协议、API Key ID、首次分组和终端用户短指纹。不保存正文、Token、Cookie 或完整会话 ID。

## 和官方粘性会话的关系

当前默认分支上，官方粘性会话仍然是 `stickySessionTTL = 1h` 的缓存。账号不可调度或模型限流时会清理这条缓存并进入其他账号。上游失败会把账号放入排除列表后重选。`gateway.max_account_switches=0` 不会禁用切换：初始化只接受大于 0 的值，否则保留默认上限 10。这些行为在严格模式关闭时保持不变。

启用严格模式后，数据库绑定才是事实来源。Redis 键 `strict_session_binding:` 只做加速，不设置 TTL。缓存清空、Redis 故障或进程重启都回源数据库。Redis 读失败不会被当成未绑定；数据库读失败或写失败会返回 `strict_session_store_unavailable`，不会先用一个还没落库的账号。

启用前已经过期的官方粘性缓存无法恢复。若缓存里还留着同一稳定会话 ID 的官方粘性记录，首次分配会把它当作选号线索；这不是历史保证。

## 配置

```yaml
gateway:
  strict_session_binding:
    enabled: false
    end_user_header: ""
    session_header: "X-Session-Id"
    same_account_retry_limit: 0
    third_party:
      enabled: false
      base_url: ""
      api_key: ""
      timeout_seconds: 0
```

环境变量与配置键对应，例如 `GATEWAY_STRICT_SESSION_BINDING_ENABLED=true`、`GATEWAY_STRICT_SESSION_BINDING_THIRD_PARTY_API_KEY`。第三方地址必须是绝对 `http` 或 `https` URL，不能把凭据写进 URL。`base_url` 是 API 根，例如 `https://relay.example.com`；程序会请求 `{base}/v1/messages`。

原账号槽位占满时，只按 `gateway.scheduling.sticky_session_max_waiting` 和 `sticky_session_wait_timeout` 在原账号上等待。等不到就进入第三方或返回明确错误。

进程内第三方成功时，本阶段不把用量记到订阅账号上，也不从 Sub2API 余额扣费。需要由 Sub2API 计费时，请关闭 `third_party.enabled`，让外层 New API 接收 `strict_session_fallback_required` 后走它自己的渠道和账单。

## 迁移、启用与回滚

迁移文件：`backend/migrations/242_strict_session_bindings.sql`。它只创建 `strict_session_bindings`。`account_id` 没有外键，删除账号不会级联删除绑定，因此「从未分配」和「原账号已不存在」可以区分。

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

`strict_session.dispatch` 以及 handler 的 `strict_session.*` 只记录原账号 ID、会话短指纹、回退原因和最终链路（`origin`、`origin_check`、`third_party`、`error`、`stream_interrupted`）。不记录正文、Token、Cookie 或完整会话 ID。

## 未覆盖范围

- Gemini 分组走 `/v1/messages` 的原有循环。
- OpenAI / Responses / Chat Completions / 图片 / 其他供应商。
- `POST /v1/messages/count_tokens` 不建立绑定。
- 启用前已过期的官方粘性会话不能追溯恢复。
- 进程内第三方的工具调用、上下文窗口和价格由该中转自己决定；不兼容的请求不会在失败后再回到订阅池。
