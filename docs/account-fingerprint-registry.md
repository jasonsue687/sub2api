# 账号身份指纹登记与绑定（第一、二阶段）

本阶段只增加观察记录和管理绑定。绑定关系尚未参与出站身份选择；原有 Redis 指纹更新、mimic、后台测试及会话绑定保持原有行为。

## 登记口径

- 在已认证请求完成 JSON 解析后、缓存合并和请求转换前登记。覆盖 Messages、count_tokens 以及 Gateway 的 Chat Completions 兼容入口；每个入站请求登记一次，上游重试不会重复计数。格式错误或未认证的请求不登记。
- 复用 `IdentityService.createNewFingerprintFromHeaders`，其内部调用既有 `createFingerprintFromHeaders`、UA 校验、版本下限与 `generateClientID`，没有复制一套指纹生成规则。原有缓存创建也使用该公共路径。
- 登记指纹是经原有逻辑标准化后的完整快照；同时保留七个实际入站身份头，便于区分客户端传值与默认值。每个入站字段最多保留 1024 字节。
- 设备标识通过原有 `ParseMetadataUserID` 解析。客户端未提供可解析设备标识时，使用原有生成函数生成登记标识；去重键排除新生成的随机标识，数据库冲突更新保留首次成功登记的标识。
- 相同来源、入站字段及标准化身份去重，累计请求次数和首次/最近登记时间。设备、UA、OS、SDK 等真实变化会形成另一条不可变快照。
- 不保存会话 ID、metadata 原文、客户端 account UUID、消息正文、凭据、Cookie 或 beta 能力头。登记错误只记录日志，最多等待 250 ms，不改变请求的准入或选择结果；数据库故障期间计数可能不完整。

## 缓存导入

启动时自动读取所有未删除的 Anthropic OAuth 账号现有 Redis 指纹；登记页面也提供“导入现有缓存身份”按钮。导入复用 `IdentityCache.GetFingerprint`，不调用 `GetOrCreateFingerprint`。

导入保留全部身份字段和 ClientID，只排除 Redis 的 UpdatedAt 元数据；不会续期、修改或删除缓存，不会生成替代设备标识，不会自动绑定账号。重复导入幂等，新的缓存身份另行登记。无缓存计为跳过，损坏或读取失败单独计数；设置 30 秒操作超时，Redis I/O 同时受既有读写超时配置约束，未完成时可以重试。

SQL 迁移：[`251_account_fingerprint_registry.sql`](../backend/migrations/251_account_fingerprint_registry.sql)。迁移只创建表；Redis 数据由应用启动时和管理端导入，不依赖 SQL 访问 Redis。

## 绑定管理

- 管理菜单“账号身份指纹”：查看来源、设备、UA、OS、SDK、运行时、登记次数、时间和绑定账号；可搜索并选择订阅账号进行绑定。
- 账号管理中 Anthropic OAuth 账号的“身份绑定”：查看当前绑定、搜索选择其他身份或解除绑定。
- 两个页面共用 `account_fingerprint_bindings` 和同一个 API。账号 ID 是主键，保证一个账号最多绑定一个身份；一个登记身份可以被多个账号选择。
- 只允许未删除的 Anthropic OAuth 账号。绑定事务锁定账号行并验证记录存在；失败不覆盖旧绑定。
- 页面和接口均明确标记为“尚未应用”。不会修改账号凭据、Redis 指纹或调度器缓存。

## 管理接口

全部位于现有管理员认证、合规和审计中间件之后：

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/api/v1/admin/account-fingerprints` | 分页查询，支持 search、source |
| GET | `/api/v1/admin/account-fingerprints/:id` | 完整指纹记录及绑定账号 |
| GET | `/api/v1/admin/account-fingerprints/accounts` | 搜索可绑定订阅账号及当前绑定 ID |
| POST | `/api/v1/admin/account-fingerprints/import-cache` | 幂等导入现有缓存 |
| GET | `/api/v1/admin/accounts/:id/fingerprint-binding` | 读取绑定，`applied=false` |
| PUT | `/api/v1/admin/accounts/:id/fingerprint-binding` | `fingerprint_id` 为正整数；显式 null 解除绑定 |

## 验证

后端回归覆盖复用生成逻辑、身份去重、会话不参与去重、登记故障不改变原有选择，以及 messages/count_tokens 在普通和 mimic 模式下的最终出站身份不变。

真实 PostgreSQL 测试读取 `FINGERPRINT_TEST_DSN`（PostgreSQL URL），在独立临时 schema 内应用真实迁移，验证并发首次登记、计数、缓存导入幂等性、缓存不被修改、绑定切换/解除及非法账号保护。未设置该变量时跳过真实数据库测试。

```sh
cd backend
go test -tags=unit ./...
go test ./internal/repository -run TestAccountFingerprintRegistryPostgres -count=1
```

前端使用仓库 CI 相同的 pnpm 9。绑定弹窗测试覆盖两个入口、解除绑定、加载失败保护和重新打开时刷新当前绑定。

```sh
cd frontend
pnpm exec vitest run src/components/account/__tests__/FingerprintBindingModal.spec.ts src/i18n/__tests__/localeKeyCompleteness.spec.ts
pnpm exec vue-tsc --noEmit
pnpm run build
```
