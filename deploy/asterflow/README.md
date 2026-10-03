# AsterFlow 构建、版本发布与生产部署

只用于 `jasonsue687/sub2api`。`main` 跟随官方，定制代码合并到 `asterflow`；默认分支为 `asterflow`。合并、生成正式 Release 与更新生产是三个独立步骤。

## 1. 自动构建

[AsterFlow Build](../../.github/workflows/asterflow-build.yml) 在 PR 上运行验证，在 `asterflow` push 或手动运行时通过验证后推送 Linux amd64 前后端合一镜像。

验证包括完整后端 unit/integration、lint、审计 race、前端 lint/typecheck/关键测试和部署策略测试。PR 无生产凭据，不发布镜像。

构建镜像为 `ghcr.io/jasonsue687/sub2api:asterflow-sha-<完整SHA>`；部署始终固定 digest。程序版本形如 `0.2.13-asterflow.3`。`asterflow-release/release.json` 记录 SHA、digest、程序版本、构建 ID/attempt 和数据库迁移哈希，Actions 制品保留 90 天；必须在到期前正式发布，过期未发布的构建需重新构建。

## 2. 将已验证的构建发布为正式版本

推荐在 Actions → **AsterFlow Release** → Run workflow，选择 `asterflow`：

- `release_tag`：例如 `asterflow-v0.2.13-r1`，表示官方 0.2.13 基线上的定制正式发布 1。
- `build_run_id`：填写一次成功的 AsterFlow Build 运行 ID。

工作流验证提交属于 `asterflow`、构建来源/attempt、Tag 的基线版本和镜像 digest 可读取；创建带说明的 Tag（已有 Tag 必须指向同一提交），将既有镜像清单保存至 Release，先上传附件，再发布为不可变 Release。不重新编译，不覆盖官方 `v*` Tag、`latest` 或程序内构建版本，不部署生产。

也支持推送 `asterflow-v*` Tag 自动发布：Tag 指向的提交必须已存在成功的分支构建，且该提交包含本发布工作流。不要在构建尚未成功时打 Tag；失败后可在 Actions 使用同一 Tag/指定构建手动重试。旧提交不包含新工作流时使用上述手动入口。

仓库必须启用 immutable releases。正式 Tag 与 Release 附件不能覆盖；修订版本使用新的 `r2`、`r3`。Release 的 `release.json` 长期保存，不依赖原 Actions 构建日志/制品的留存；GHCR 镜像必须同时保留，不要删除仍在使用或计划回退的 digest。Release 不会替 GHCR 锁定或保存镜像，也不会创建同名镜像标签。

如果发布失败遗留草稿，先检查 Tag/附件后处理；流程不会覆盖未知草稿。已发布的同一版本只允许相同制品信息，不允许替换。正式发布清单下载后同时检查 API SHA-256、Tag→提交关系和 GitHub 的 release asset attestation。

## 3. 选择正式版本上线

Actions → **AsterFlow Deploy** → Run workflow，选择 `asterflow`：

| 参数 | 用法 |
|---|---|
| `release_tag` | 已发布的正式 Tag，例如 `asterflow-v0.2.13-r1` |
| `operation=preflight` | 显示当前镜像、目标版本、待迁移列表、回退候选及是否无需重复发布 |
| `operation=deploy` | 校验、备份、更新应用并验收 |
| `allow_migrations` | 有新增迁移时须显式勾选；不会绕过历史迁移不一致或允许数据库降级 |

预检使用 `environment.deployment: false`，仍受 production 环境限制并使用其凭据，但不会生成生产部署记录。真正 deploy/rollback 才生成部署记录；工作流名称显示动作和目标 Tag。

发布使用 Release 保存的固定 digest。镜像和受管采集设置一致、迁移一致且容器/HTTP/版本健康时返回 `unchanged`，不拉镜像、不备份、不重启；只更新受限目录内的版本记录。

实际发布：拉镜像并核对平台/来源/SHA/版本 → 停止应用写入 → 备份数据库、Compose、环境文件和应用数据（排除日志）→ 检查 `pg_restore --list` 和 SHA-256 → 复核迁移未被并行更改 → 写入受控 override → 只重建 `sub2api` → 检查容器/HTTP/版本/镜像 ID/迁移以及 PostgreSQL、Redis 容器与启动时间未变 → 记录成功版本。

备份在中央 `/opt/sub2api/backups/asterflow-<UTC时间>-<SHA>/`，权限 0700，包含凭据与数据库，不上传 GitHub。备份目录可读性检查不是完整恢复演练。首次跨版本升级须另外检查迁移、旧程序兼容性与恢复步骤。

成功版本记录为 `/opt/sub2api/automation/state.json`：保存当前及上一个成功发布的 Tag、digest、迁移清单和备份路径，不保存 registry token。失败、预检不会推进成功版本；重复部署同一个 digest 不覆盖上一个版本。status 同时给出真实容器镜像/健康与记录中的版本，不能用记录替代实际容器状态。

## 4. 回退

仍在 **AsterFlow Deploy**，先选择 `preflight-rollback`，通过后选择 `rollback`：

- `release_tag` 留空：通常选择上一个成功上线的版本；若实际容器已被失败的发布替换、成功记录尚未推进，则选择最近一次成功版本。没有记录时明确报错。
- 填写 Tag：选择一个具体的已发布不可变 Release。
- 可以回退已停止或不健康的应用；仍要求现有容器存在、配置来源正确、数据库可访问、磁盘足够、镜像与 Release 校验通过。
- 回退要求目标与当前数据库的迁移文件/哈希完全相同，不能新增、删除或修改迁移。`allow_migrations` 不放宽这个要求。
- 回退保留当前数据库，也会先备份。回退失败时不会自动重新启动原本已知不健康的程序。

这里的回退是应用镜像恢复，不自动恢复数据库。跨迁移降级仍需独立兼容性验证/人工恢复；恢复旧数据库备份可能丢失备份之后的数据。

首次从旧生产 `0.2.8-anthropic-monitor` 切换时，旧镜像没有本流水线的不可变 Release，因此没有可自动选择的旧 Tag；第一次正式发布也不能凭空生成旧版本回退记录。须使用该次备份及旧镜像另行制定恢复方案。新流程只为以后经过本流程成功上线的正式版本建立记录。

## 5. 失败处理与配置边界

- 新程序未启动，或无新增迁移且失败后迁移哈希完全未变：可以恢复原 override 和此前健康的应用。
- 新增迁移后新程序一旦尝试启动，或数据库历史发生变化：失败时停止新应用，保留备份和数据库，人工处理，不启动可能不兼容的旧程序。
- GitHub concurrency 和服务器文件锁防止并行发布。取消任务会尝试同样的恢复；断电/SIGKILL 仍需人工检查。
- 受控配置是 `/opt/sub2api/docker-compose.override.yml`，未知 override 拒绝覆盖。显式 `-f` 运维命令必须同时包含主文件和 override。
- 只修改应用镜像及 `SUB2API_ANTHROPIC_AUDIT_ENABLED=true`、`SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS=*`、Ops 监控/清理环境开关。管理设置显式关闭 Ops/清理时拒绝发布。不修改账号、OAuth、代理、数据库凭据、端口或挂载。
- 验收不自动发送模型请求，不消耗模型配额；应用健康不代表订阅账号可用。

## 一次性接入及维护

GitHub Environment `production` 仅允许 `asterflow` 分支：

| 类型 | 名称 | 内容 |
|---|---|---|
| Secret | `DEPLOY_SSH_KEY` | 专用部署私钥，不能使用个人密钥 |
| Secret | `DEPLOY_KNOWN_HOSTS` | 可信 SSH 链路核验的跳板/中央公钥 |
| Variable | `DEPLOY_JUMP_HOST` | 已核验跳板公网 IP |
| Variable | `DEPLOY_TARGET_HOST` | 已核验中央私网 IP |

SSH 使用 `asterflow-prod-app-01` 与 `asterflow-prod-account-central-01`。跳板公钥采用 `restrict`，forced command 固定 `/usr/bin/nc <中央私网IP> 22`，只提供固定目标的字节通道；客户端使用 ProxyCommand，不允许任意命令、SSH 端口/agent/X11 转发或 PTY。中央 forced command 只运行 root 持有的 `/opt/sub2api/automation/deploy.py`，允许 `status`、`preflight`、`deploy`、`preflight-rollback`、`rollback`。

`deploy.py` 与 `manifest.py` 须经管理员测试、单独安装；业务构建不会替换生产入口。保留旧入口备份，安装时先更新向后兼容的 manifest，再原子替换 deploy。不得关闭 SSH 主机密钥校验或临时公开管理端口。

## 本地验证

在源码仓库根目录：

```bash
python3 -m unittest discover -s deploy/asterflow -p 'test_*.py' -v
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 .github/workflows/asterflow-*.yml
```

集成测试和构建需要 Docker。发布前须通过 CI，不使用反复重跑绕过失败。
