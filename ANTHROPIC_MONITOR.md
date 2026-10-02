# Anthropic 订阅账号出站请求监控

## 入口与采集开关

管理员菜单新增 **Anthropic 请求监控**，路径 `/admin/anthropic-requests?account_id=11`。页面可查询最近 1 小时、24 小时或 7 天，自动刷新、筛选不一致、查看摘要、比较两条请求、导出当前页。

采集默认关闭。部署此版本后，需要在应用进程设置以下非秘密环境变量，并重建应用容器使之生效：

```env
SUB2API_ANTHROPIC_AUDIT_ENABLED=true
SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS=11
```

多个账号用逗号分隔。空列表、无效 ID 或未开启总开关均不会变成全账号采集。管理员 Ops 监控开关也须开启才能查询。停止采集可移除上述环境变量并重建应用容器；已写入记录仍遵循系统日志留存和清理设置。

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

使用现有 `ops_system_logs`，component 为 `audit.anthropic_outbound`，摘要放在 `extra.audit`；无数据库迁移和新公开端口。新 API `/api/v1/admin/ops/anthropic-requests` 位于现有管理员鉴权路由组。必须提供正数 account_id；时间最多 7 天、每页最多 100 条，数据库查询超时 10 秒。

汇总和组合统计覆盖整个时间范围；“仅显示不一致”只筛选明细。单条 SQL 保证汇总、分页与组合处于同一数据库快照。组合列表展示前 20 种。按 account_id/created_at 使用已有索引。

复用现有有界异步日志队列和清理策略。采集为尽力写入，进程崩溃、队列满或数据库故障可能丢记录，不能用于精确计费。页面显示日志队列的全站进程级丢失/写入失败累计值；多实例部署不应把单实例健康值当作全局零丢失证明。

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
