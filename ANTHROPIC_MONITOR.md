# Anthropic 请求监控

管理员菜单 **Anthropic 请求监控** 路径为 `/admin/anthropic-requests`。列表每一行是一次客户端请求（`client_request_id`），详情页左右对照入站正文和发往 Anthropic 的出站正文。入站保留原始参数，省略三个指定字段并掩码凭据；出站沿用提示词和工具脱敏规则。`/v1/chat/completions` 与 `/v1/responses` 本阶段不采入站，若它们仍转到 `/v1/messages`，列表标为「无入站记录」。选路失败、没有发出上游请求的入站标为「无出站请求」。

## 入站与出站全文

- 入站中间件位于认证之后、分组模型白名单和合成路由之前，在模型替换、JSON 容错修正和协议转换之前复制原始参数。按端点采集，不按客户端或 User-Agent 筛选：Claude Code、CLI、curl、SDK 及无 User-Agent 的客户端一视同仁。覆盖 POST `/v1/messages`、`/v1/messages/count_tokens`、`/messages/count_tokens`、`/antigravity/v1/messages` 和 `/antigravity/v1/messages/count_tokens`；认证失败和读取 body 失败的请求不采集。
- 入站 JSON 顶层 `messages`、`tools`、`tool_choice` 如存在，整个值替换为字符串 `"[OMITTED]"`，不保存内部结构、内容、数量、长度或哈希；原本不存在的字段不补入。`system`、`metadata.user_id`、model、生成参数、布尔值、零值及未知参数保留客户端原值。识别到的凭据字段和 Authorization、x-api-key、Cookie 等请求头继续掩码。入站参数摘要也不保存这三个省略字段的统计或类型。
- 入站请求头保留接收时的值（凭据除外），含原始 session 标识和重复值；对照视图合并显示重复值，复制时保留各项。压缩正文先解压，原始 Content-Encoding 等请求头单独保留。此处的“原始”指应用层参数，不是 HTTP 报文字节；HTTP 库会规范化头名，数据库 JSONB 会规范化 JSON 排版、键顺序和重复键。采集副本的替换不影响实际转发请求。
- 出站在 `HTTPUpstream.Do` / `DoWithTLS` 里、现有摘要采集旁边再复制一份即将发送的请求；只有原摘要采集接受的请求才写出站全文，范围仍是直连 `api.anthropic.com` 的 `/v1/messages` 与 `/v1/messages/count_tokens`。
- 一次入站对应多次出站。出站带 `attempt_seq` 与 `retry_reason`（`initial`、`account_switch`、`same_account_retry`、`upstream_retry`、`signature_rectify`、`budget_rectify`）。
- 热路径只复制 header 和最多 8MiB 的 body，然后非阻塞写入容量 512 的队列。队列满则丢弃并计数，不拖慢线上请求。后台 worker 再脱敏并落库。
- 出站脱敏替换提示词、密钥和工具名。`x-anthropic-billing-header:` 原文保留。thinking signature 只留长度和 SHA-256。工具名（含 `tool_use` / `tool_choice` 的 name）只留长度，不留哈希。
- 入站和出站继续使用现有上限：原始 body 超过 8MiB 时只记 `too_large` 状态；无法解析的原始 JSON 只记 `invalid_json`，不把修正后的 JSON 当作原始参数保存；处理后的单份文档超过 64KiB 时只存截断预览。记录中的 `original_bytes` 是整个请求体的长度。这些异常记录不能视为完整请求。
- 新表 `anthropic_request_captures`（迁移 250）。列表查询不读取 body。保留 30 天，由 Ops 清理任务删除。原 `ops_system_logs` 摘要和 `GET /admin/ops/anthropic-requests` 保持不变。新页面使用 `GET /admin/ops/anthropic-request-sessions` 与 `.../detail`。
- 入站开关 `SUB2API_ANTHROPIC_INBOUND_AUDIT_ENABLED` 默认开启，与出站总开关独立。账号白名单仍用 `SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS`；尚未选中账号的失败入站（account id 为 0）会保留。
- 入站账号范围和尝试数不依赖出站采集开关；即使某次出站不保存全文，也会记录其实际账号和尝试。列表和详情的会话一致性仅汇总出站尝试：存在 mismatch 即标为不一致，否则有 unknown、空状态或已知缺失采集即标为未知，全部出站 matched 才标为匹配；入站判定不参与。详情单独展示当前选中出站的 UA、billing 入口/版本和具体原因。
- 手工清理系统日志时，平台、日志级别、主机和文本搜索条件无法映射到采集表，因此带这些条件的操作不删除采集记录；可映射的请求 ID、账号、用户、模型及时间条件按原范围清理。30 天定时保留策略不变。

## 出站一致性检查与历史统计

从请求列表或详情点击“出站一致性 / 历史统计”，进入 `/admin/anthropic-outbound`。按订阅账号查询原 `ops_system_logs` 摘要，覆盖保留期内的历史及当前出站；恢复一致、不一致、未知统计、身份参数组合、逐次出站的原因、两条摘要对比与自动刷新。不补造历史入站或正文，不改变入站原始参数的采集与省略规则。

## 原出站摘要

## 入口与采集开关

下面这一节描述仍写入 `ops_system_logs` 的出站摘要，以及未改动的旧查询 API。入站/出站对照页面使用新采集 API，独立出站一致性页面继续使用该摘要 API。

所有符合采集范围的 Anthropic 订阅账号默认开启，无需逐个配置 ID；新增账号自动纳入。范围仍为直连 `api.anthropic.com` 的订阅请求，API Key 和自定义中转地址不在此监控范围内。

可选部署覆盖（修改后需重建应用容器）：

```env
# 关闭所有采集（未设置或空值默认开启）
SUB2API_ANTHROPIC_AUDIT_ENABLED=false
# 如需仅采集指定账号，可设置逗号分隔的白名单；未设置、空值或 * 表示全部
SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS=11,12
```

已有部署若仍保留 `SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS=11`，会继续只采集账号 11；升级时删除该变量或设置为 `*` 才能采集全部。显式关闭总开关优先，非空无效开关或无效 ID 列表不会扩大采集范围。管理员 Ops 监控总开关仍须开启才能查询。

监控记录保留滚动 **30 天**，页面支持最近 1 小时、24 小时、7 天、30 天查询。定时 Ops 清理任务按 `audit.anthropic_outbound` 组件单独清理早于 30 天的记录，普通系统日志仍沿用原有保留期；即使普通日志保留 7 天或执行定时全清，也不会提前清理监控记录。Ops 清理任务须启用（默认已启用），过期记录在下一次计划执行时删除。管理员主动手工清理仍可提前删除记录。停止采集不删除已有记录，也不能补录启用前的数据。

## 数据与判定边界

- 采集标准 gateway messages、count_tokens 和管理员账号测试构造的 OAuth 订阅请求，且最终目标必须为 `api.anthropic.com` 对应端点。API Key、其他供应商及自定义域名不采集。
- 采集位置是 HTTPUpstream 真正调用 HTTP 客户端的边界；普通与 TLS 指纹路径都覆盖，TLS 委托不会重复计数。不改变原始 headers、body 或路由，不额外请求 Anthropic。
- 每个应用层发送尝试一个 UUID；重试单独记录。统计的“入口请求”按非空 client_request_id 去重，无关联 ID 的尝试独立计数。HTTP 库内部透明重试、重定向、TCP/TLS 连接不逐个计数。
- 收到响应头或传输错误后异步落库；进行中请求尚未显示。时间范围按落日志时间筛选，表格同时保留发送开始时间。HTTP 200 不证明 SSE 全程成功，也不是服务商侧抓包或身份判定。
- 保存白名单请求头：User-Agent、anthropic-version/beta、x-app、Stainless SDK/运行时/系统/重试/超时字段。每个头最多 1024 字节；不采集 Authorization、Cookie 等凭据。
- 参数摘要包含 model、service_tier、stream、max_tokens、temperature、top_p、top_k、thinking.type/budget_tokens、output_config.effort、tool_choice.type，以及 messages/tools/stop_sequences 的数量。不会存消息、system 文本、工具名称/描述/schema、停止字符串或响应正文。
- 只从 billing 声明提取 cc_version/cc_entrypoint；metadata 中 device/account/session 仅保存 SHA-256 哈希。标识哈希仍仅供管理员访问，不应当作匿名数据公开。
- 请求体经独立 GetBody 副本解析，上限 8 MiB。超过上限、无 GetBody 或 JSON 无效时明确记为未知，原请求继续发送。参数对比只覆盖已采集的摘要，无法证明完整请求体相同。
- 一致性只检查可解析 Claude CLI UA 的入口、版本与 billing 声明；cc_version 的消息指纹后缀不当作 CLI 版本。未能解析或缺少字段记为 unknown；多 billing 块记为不一致。此处不推测服务商风控规则。
- 身份组合是白名单身份字段组合，不等于不同用户/设备数量。它排除 session、生成参数、重试次数、超时和 cc_version 消息指纹后缀；beta 等能力字段不同仍可能形成多个组合。

## 存储、权限与查询

出站摘要仍使用现有 `ops_system_logs`，component 为 `audit.anthropic_outbound`，摘要放在 `extra.audit`。旧 API `/api/v1/admin/ops/anthropic-requests` 位于现有管理员鉴权路由组。必须提供正数 account_id；时间最多 30 天、每页最多 100 条，数据库查询超时 10 秒。全文对照存在独立表，见上文。

汇总和组合统计覆盖整个时间范围；“仅显示不一致”只筛选明细。单条 SQL 保证汇总、分页与组合处于同一数据库快照。组合列表展示前 20 种。按 account_id/created_at 使用已有索引。

复用现有有界异步日志队列，监控记录使用独立的 30 天定时清理策略。采集为尽力写入，进程崩溃、队列满或数据库故障可能丢记录，不能用于精确计费。页面显示日志队列的全站进程级丢失/写入失败累计值；多实例部署不应把单实例健康值当作全局零丢失证明。

## 本地预览与验证

从本仓库根执行（先按当前 CI 使用 pnpm 9 安装锁文件依赖）：

```sh
cd frontend
node preview-anthropic-monitor.mjs
```

只监听 `127.0.0.1:5178`，显著标注全部为合成数据，无生产 API 连接。演示覆盖 cli/local-agent 不一致、匹配、HTTP 429 和传输失败，复用正式页面组件；不作为真实流量证据。

历史验证（原始实现基线 `fd80b08c9`，2026-09-27）：包含前端静态资源的 Linux amd64 应用编译成功；前端生产构建、完整前端 lint、页面专项测试 5 项；后端完整 unit 套件、采集 race 测试、传输与 TLS 委托计数测试、查询与参数校验测试。另在临时 PGlite PostgreSQL 引擎中验证 145 条合成记录的全窗口汇总、去重、账号隔离、分页、空结果及 mismatch 过滤。这些历史结果不替代后续基线上的重新验证。

上述历史验证中，完整 integration 套件因本机 Docker 服务不可用而未通过；不宣称这部分已验证。后续基线应在具备 Docker 的 CI/隔离构建环境补跑。

2026-10-02 将原始补丁 `67c2b96fa` 移植到 fork 的 `main` 基线 `458b92abd`，用于合并请求审阅；本次移植没有部署或修改生产服务。该基线上的验证结果以合并请求和 CI 记录为准。
