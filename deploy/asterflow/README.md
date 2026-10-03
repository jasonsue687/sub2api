# AsterFlow 构建与生产发布

两条流水线仅用于 `jasonsue687/sub2api` 的 `asterflow` 分支；`main` 继续保留用于同步官方。正式代码先合并到 `asterflow`，生产发布是独立的手动操作。

## 构建

[AsterFlow Build](../../.github/workflows/asterflow-build.yml) 在 `asterflow` 的 push/PR 上运行，亦可手动运行。

- 完整后端 unit、integration，审计包 race，前端 lint/typecheck/关键测试，以及部署策略测试全部通过后，才允许发布镜像。
- PR 只验证，不取得生产环境 Secret，不推送镜像。
- 正式分支构建 Linux amd64 前后端合一镜像到 `ghcr.io/jasonsue687/sub2api:asterflow-sha-<完整提交>`。部署使用 digest，不使用可变标签。
- 产物 `asterflow-release/release.json` 保存 SHA、digest、版本、运行 ID/attempt 和数据库迁移校验值，留存 90 天。超过留存期重新构建，不手工拼接制品。
- 版本形如 `0.2.13-asterflow.1`，后缀取工作流运行编号，不覆盖上游版本标签或 `latest`。

## 一次性接入

GitHub 手动工作流要求文件存在于默认分支。因此 fork 默认分支使用 `asterflow`，工作流合并后出现在 Actions 页面。

创建 GitHub Environment `production`，仅允许 `asterflow` 分支。配置：

| 类型 | 名称 | 内容 |
|---|---|---|
| Environment Secret | `DEPLOY_SSH_KEY` | 专用部署私钥；不得复用个人 SSH 密钥 |
| Environment Secret | `DEPLOY_KNOWN_HOSTS` | 已通过可信 SSH 链路核验的跳板/中央节点公钥，别名为下述两个规范节点名 |
| Environment Variable | `DEPLOY_JUMP_HOST` | 主站跳板的已核验公网 IP |
| Environment Variable | `DEPLOY_TARGET_HOST` | 中央 Sub2API 的已核验私网 IP |

SSH 固定使用 `asterflow-prod-app-01` 与 `asterflow-prod-account-central-01`。不关闭主机密钥检查，不公开中央节点端口。

将本目录 `deploy.py` 和 `manifest.py` 安装到中央节点 `/opt/sub2api/automation/`，目录及文件由 root 持有，普通用户不可写。私钥留在 GitHub Environment，公钥采用以下限制：

- 跳板：`restrict` 禁止 SSH 转发及任意命令；forced command 固定执行 `/usr/bin/nc <中央私网IP> 22`，只提供通向中央 SSH 的字节通道。客户端使用 `ProxyCommand`，不能选择其他目标或建立反向端口转发。
- 中央：禁止端口/agent/X11 转发与 PTY；forced command 只运行 root 持有的 `deploy.py`，参数作为一个字符串传入后只允许 `status`、`preflight`、`deploy`。不接受远程脚本上传或任意 shell。
- 此入口具有更新中央应用的权限；管理员修改入口时需重新审阅、安装并验证。业务构建不会自动替换服务器部署脚本。

## 发布操作

在 Actions → AsterFlow Deploy → Run workflow 选择 **asterflow**：

1. `build_run_id`：填写成功的 AsterFlow Build 摘要中的运行 ID。
2. `operation=preflight`：校验制品来源、SHA 属于已合并分支、生产主机/配置/健康、磁盘、迁移历史和 Ops 清理设置。不会拉镜像或重启服务。
3. 核对结果后选择 `operation=deploy` 发布同一制品。有新增迁移时，默认拒绝，须核验迁移后明确设置 `allow_migrations=true`。

生产步骤：拉取指定 digest 并核对平台/来源/SHA/版本 → 停止应用写入 → 在受限目录备份数据库、Compose、环境文件和应用数据（排除日志）→ 校验 `pg_restore --list` 及文件 SHA-256 → 写入受控 Compose override → 仅重建 `sub2api` → 检查健康、版本、实际镜像、迁移哈希以及 PostgreSQL/Redis 容器与启动时间未变。

备份位于中央 `/opt/sub2api/backups/asterflow-<UTC时间>-<SHA>/`，不上传到 GitHub；包含数据库和凭据，目录权限为 0700。备份目录列表可读性检查不等同于完整恢复演练，首次跨版本迁移仍需单独演练。

持久 override 为 `/opt/sub2api/docker-compose.override.yml`，不会改写原 Compose 或 `.env`。普通 `docker compose` 会读取它；显式传 `-f` 的运维命令必须同时带主文件和该 override。如果已存在不属于本流水线的 override，发布会停止，不覆盖。

发布同时显式设置 `SUB2API_ANTHROPIC_AUDIT_ENABLED=true`、`SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS=*` 和 Ops 环境开关；若管理设置显式关闭监控/清理，则预检拒绝并提示。不会修改账号、OAuth、代理、数据库凭据、端口、挂载或其他服务。

健康验收不代表模型请求/订阅账号可用。流水线不自动消费模型配额，真实业务验收由发布后已有生产流量或明确授权的测试完成。

## 失败处理

- 备份或配置校验失败且尚未启动新程序：恢复原 override 并启动旧应用。
- 未新增迁移且失败后数据库迁移哈希完全未变：自动回切旧应用。
- 允许新增迁移后，新应用一旦尝试启动：失败时停止新应用并保留备份及数据库，要求人工处理；绝不自动恢复数据库或启动可能不兼容的旧程序。
- GitHub 发布任务和服务器文件锁都避免并发发布。取消任务会尝试走同样的恢复逻辑；机器断电、强制 SIGKILL 等仍需人工检查备份记录与容器状态。

## 本地验证

在源码仓库根目录执行：

```bash
python3 -m unittest discover -s deploy/asterflow -p 'test_*.py' -v
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 .github/workflows/asterflow-build.yml .github/workflows/asterflow-deploy.yml
```

集成测试和镜像构建需要 Docker。没有 Docker 的本机不能替代 Linux CI 验证；任何测试失败都阻止制品发布，不自动重试至通过或绕过测试。
