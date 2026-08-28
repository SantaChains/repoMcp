# repoMcp（SantaChains 改版）

IM 机器人 / MCP 客户端的仓库源码检索与 issue 管理服务。让 LLM 查源码、带可核验出处回答、代提 issue。

- 传输：Streamable HTTP，单端点 `POST /mcp`，Bearer 鉴权
- 检索：本地 clone + BM25 倒排索引 + 正则符号表。**无 embedding、无向量库、无外部服务**
- issue：GitHub REST，建/评/改状态与标签，**无删除端点**，强制查重与限频
- 依赖：**零第三方 Go 包**，`CGO_ENABLED=0` 单二进制。运行期仅需系统 git

> 基于 [zerx-lab/repoMcp](https://github.com/zerx-lab/repoMcp)（MIT）改版，原始设计全部保留。改版新增：多仓并行同步、浅克隆、结构化日志 + trace-id、配置强校验、敏感词过滤、HTTP 超时与优雅关闭、/sync 手动同步、issue 三层限频。

## 工具

**检索（始终可用）**

| 工具 | 用途 |
|---|---|
| `repo_overview` | 技术栈、规模、目录结构、README 摘要 |
| `search_code` | BM25 关键词检索，支持 repo / lang / path_glob 过滤 |
| `read_file` | 按行范围读原文，`blame: true` 附每行最后提交与作者 |
| `find_symbol` | 按名字查定义（跨命名风格归一），比 search_code 精确 |
| `git_history` | 提交历史，附 commit permalink |

**issue（配了 `repos[].issues` 才挂载）**

| 工具 | 用途 | 前提 |
|---|---|---|
| `search_issues` | 查已有 issue（双路召回：GitHub 搜索 + 本地重叠系数打分） | `issues` 已配 |
| `read_issue` | 读正文 + 最近评论 | 同上 |
| `create_issue` | 代用户提交，正文服务端按模板渲染 | `issues.write: true` |
| `update_issue` | 追加评论、关闭/重开、增删标签 | 同上 |

initialize 握手时下发 instructions：可用仓库清单、各仓 issue 能力、工具选择规则，要求回答必须引用来源、检索无果不得编造。

## 配置

复制 `config.example.json` 为 `config.json`：

```json
{
  "listen": ":8790",
  "token": "MCP 共享长随机串",
  "dataDir": "./data",
  "syncInterval": "15m",
  "maxResponseBytes": 12000,
  "githubToken": "GitHub PAT",
  "maxIssueCreatesPerHour": 5,
  "repos": [
    {
      "name": "lumi_books",
      "desc": "Kotlin Android 应用",
      "url": "https://github.com/huangder/Lumi_Books.git",
      "ref": "main",
      "webBase": "https://github.com/huangder/Lumi_Books",
      "exclude": ["**/build/**", "**/*.g.kt"],
      "issues": { "slug": "huangder/Lumi_Books", "write": true }
    }
  ]
}
```

| 字段 | 说明 |
|---|---|
| `listen` | 监听地址，默认 `:8790` |
| `token` | MCP Bearer 令牌；留空不鉴权，仅限 `127.0.0.1` |
| `dataDir` | clone 存放目录，默认 `./data`。**不要指向开发工作树**——同步会 `reset --hard` + `clean -fd` |
| `syncInterval` | 后台 fetch + 重建索引周期；`"0"` 关闭 |
| `gitTimeout` | 单条 git 命令超时，默认 `3m` |
| `maxResponseBytes` | 单次工具返回字节预算，默认 12000 |
| `githubToken` | issue 工具的 PAT。留空公开仓只读可工作，私有仓/写操作必填 |
| `githubApiBase` | API 根地址，默认 `https://api.github.com`；GHE 填 `https://<host>/api/v3` |
| `maxIssueCreatesPerHour` | 单仓每小时创建上限，默认 5，`0` 不限 |
| `repos[].name` | 仓短名，`^[a-z0-9][a-z0-9._-]{0,63}$` |
| `repos[].desc` | 一句话说明，下发到 instructions |
| `repos[].webBase` | permalink 前缀；留空从 `url` 推导 |
| `repos[].include` / `exclude` | 通配符过滤，`*` 不跨 `/`、`**` 跨 `/`、`?` 单字符 |
| `repos[].dir` | 覆盖本地路径 |
| `repos[].issues` | 省略 = 该仓无 issue 能力；`{}` = 只读；`{"write": true}` = 可创建与管理 |
| `repos[].issues.slug` | `owner/repo`，留空从 `webBase` / `url` 推导；推不出**启动失败** |
| `repos[].issues.token` | 覆盖全局 `githubToken`（跨组织多 PAT） |
| `repos[].issues.labels` | 允许模型使用的标签白名单；留空以仓现有标签为准 |

环境变量覆盖：`REPOMCP_CONFIG` / `REPOMCP_LISTEN` / `REPOMCP_TOKEN` / `REPOMCP_DATA` / `REPOMCP_GITHUB_TOKEN`。

私有仓 clone：URL 内嵌 token（`https://x-access-token:<PAT>@github.com/owner/repo.git`）或宿主预配凭据助手。服务强制 `GIT_TERMINAL_PROMPT=0`，凭据缺失直接失败。

## 运行

```bash
go run . -config config.json
```

启动参数：

| 参数 | 作用 |
|---|---|
| `-config <path>` | 配置路径，默认 `config.json` |
| `-version` | 打印标题版本后退出 |
| `-print-config` | 打印**脱敏**后的最终配置（token/PAT 首尾各几字符可见）后退出 |
| `-check-config` | 仅校验配置，打印摘要后退出，不启动——适合 CI 预检查 |

启动后立即可用，首次 clone 与索引在后台。未就绪时工具明确回复「索引进行中」而非空结果。

端点：

| 端点 | 鉴权 | 说明 |
|---|---|---|
| `POST /mcp` | Bearer | MCP 主端点 |
| `GET /healthz` | 无 | 探活，返回每仓 HEAD/文件数/符号数/issue 能力。未全部就绪返回 503。附带 `lastSyncDurationMs` / `nextSyncInSeconds` 等运维字段 |
| `POST /sync` | Bearer | 手动触发同步。`{"blocking":false}`（默认）返回 202 scheduled；`{"blocking":true}` 等本轮结束后返回 200 |

请求体上限 1 MB（超限 413）。MaxHeaderBytes 1 MB。CORS 允许 `POST, OPTIONS`，暴露 `X-Trace-ID` 响应头。

## 接入 AstrBot

打开 AstrBot 管理后台 → 扩展 → MCP（`http://localhost:6185/#/extension/mcp`）→ 新增服务器：

```json
{
  "name": "repomcp",
  "transport": "streamable_http",
  "url": "http://127.0.0.1:8790/mcp",
  "headers": {
    "Authorization": "Bearer <config.json 的 token 字段值>"
  },
  "timeout": 60
}
```

| 字段 | 说明 |
|---|---|
| `transport` | **必须 `streamable_http`**。写其它值直接失败 |
| `url` | repoMcp `/mcp` 端点，端口与 config.json `listen` 一致 |
| `Authorization` | `Bearer ` + token 裸串，前缀不能省 |
| `timeout` | 秒，建议 ≥ 60（大仓首次同步或 `git_history` 深查询需 30–50 秒） |

AstrBot 管理后台 6185 端口只是粘贴配置的 WebUI，不是 MCP 通信地址。repoMcp 必须本地启动监听 `:8790`，AstrBot 才会调用。

排障：先 `curl http://127.0.0.1:8790/healthz` 看 `ready` 与每仓 `error`（无需鉴权），再用 `mcp_probe.py` 验证 MCP 握手。

## GitHub Token 权限

两层权限模型必须同时满足才能写入：

| 层 | 判定主体 |
|---|---|
| 账号层 | 你对目标仓是 owner / collaborator / member，还是外部访客 |
| Token 层 | PAT 的 scope（Classic）或 permissions（fine-grained） |

任何一层不足，GitHub 返回 403 `Resource not accessible by personal access token`。

### Classic vs fine-grained

| 维度 | Classic（`ghp_`） | fine-grained（`github_pat_`） |
|---|---|---|
| 粒度 | `public_repo` 一勾 = 所有公开仓完整读写 | 按仓 + 按功能 |
| 他人公开仓写 issue | **可以**——对公开仓自动授予 issue 读写，不需 collaborator | **不行**——必须是 collaborator |
| 适用场景 | 跨多他人公开仓发 issue | 只给自己拥有的仓；需精确控制 |
| GitHub 推荐 | legacy | 推荐 |

### 场景 → 选型

| 场景 | 推荐 | scope |
|---|---|---|
| 自己拥有的仓代发 issue | fine-grained | Issues Read+write + Contents Read-only（按仓） |
| 多个他人公开仓发 issue | **Classic `public_repo`** | `public_repo` |
| 只读 issue | 任意类型甚至不填 | fine-grained: Issues Read-only；Classic: 不勾 |
| 私有仓 | Classic `repo` 或 fine-grained 按仓勾 | 同上 |

### per-repo token 覆盖

`repos[].issues.token` 可覆盖全局 `githubToken`，跨组织多 PAT 时使用。

### 验证

```bash
# 读权限（任何 token 能过）
curl -s -H "Authorization: Bearer <token>" \
  https://api.github.com/repos/<owner>/<repo>/issues

# 写权限（会创建，记得立即关闭）
curl -s -X POST -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"title":"[test] ignore","body":"will close"}' \
  https://api.github.com/repos/<owner>/<repo>/issues
```

201 = 两层都够；403 = 至少一层不足，看错误消息是 `Resource not accessible`（账号层）还是 `Missing scope`（token 层）。

## issue 护栏

消费方是 IM 小模型，工具描述只是建议。凡是能硬校验的都在服务端拦：

| 护栏 | 行为 |
|---|---|
| 强制查重 | 创建前双路召回（GitHub 搜索 + 本地重叠系数 ≥ 0.55），命中拒绝并列候选；模型带 `confirm_not_duplicate=true` 可重试 |
| 三层限频 | 单仓 `maxIssueCreatesPerHour` + 全局 60/h + 同 reporter FNV-1a 哈希桶 10/h。配额在调 GitHub 之前扣 |
| 输入封顶 | title 6–200、body 20–8000、comment 10–4000、单次标签 ≤20 |
| 必填调研结论 | `confidence=confirmed` 时 `evidence` 无 `路径:行号` 出处直接拒绝 |
| 正文服务端渲染 | 现象 → 调研结论 → 复现 → 环境 → 来源，结构固定 |
| 标签白名单 | 只有仓现有或配置白名单里的标签被采用 |
| 状态合法性 | close 必须 comment（≥10 字）+ reason ∈ {completed, not_planned}；reopen 必须 comment 且**不能**带 reason |
| 多可写仓时 repo 必填 | 不省略、不退而求其次 |

机器人提交的 issue/评论带来源标注（提交人 + repoMcp + 索引 commit）。

## 安全

- Bearer 用 `crypto/subtle.ConstantTimeCompare` 比较
- 请求体 1 MB + MaxHeaderBytes 1 MB
- 结构化 JSON 日志 + trace-id，写入响应头 `X-Trace-ID`
- 错误消息统一脱敏：GitHub PAT 家族 + Authorization/Bearer/Token 前缀值 + 配置中裸 token → `<REDACTED>`
- `/mcp` panic 被 recoverMiddleware 捕获，返回 `-32603 internal error`
- SIGINT/SIGTERM → `http.Shutdown`（30s）→ `Store.Shutdown()`
- git 子进程信号量 2–6 并发，stdout/stderr 各 128 MiB 封顶
- 源码侧只读：不执行仓库代码、不接受绝对路径、拒绝 `..`。唯一写入面是 issue（无删除端点）
- 私有仓源码送入 LLM：确认模型数据策略，服务绑定在内网或 `127.0.0.1`

## 验证

`mcp_probe.py` 走完整 MCP 流程（initialize → tools/list → tools/call → ping）：

```bash
uv run --with mcp --with httpx --with anyio python mcp_probe.py
```

线上覆盖：`REPOMCP_PROBE_URL=... REPOMCP_PROBE_TOKEN=...`

## 局限

- 符号提取是正则启发式（非 AST），保住零依赖代价是复杂泛型/宏生成的定义可能漏抽；`search_code` 全文检索不受影响
- 索引常驻内存，百万行级留意进程内存
- git clone `--depth 1` 首次 + `--depth 50` 增量 fetch，`git_history` 默认 50 层

---

## 原始项目 vs 本改版

| 维度 | [zerx-lab/repoMcp](https://github.com/zerx-lab/repoMcp) | SantaChains 改版 |
|---|---|---|
| 架构 | Go 本地 clone + BM25 + Streamable HTTP | 相同 |
| 多仓同步 | 串行 | goroutine 并行，耗时 ≈ max(各仓) |
| git clone | `--depth 200` | `--depth 1` 首次，`--depth 50` 增量 |
| 指令词缓存 | 每次 initialize 重新拼接 | 启动 + 同步后重建，initialize 零分配 |
| 工具数量 | 5 检索 + 4 issue + PR | 5 检索 + 4 issue（PR 工具暂未恢复） |
| 配置校验 | 弱校验 | 11 项强校验 + -check-config / -print-config |
| issue 限频 | 单仓每小时 | 单仓 + 全局 + reporter 三层 |
| LICENSE | 无 | MIT（双版权） |

## 未来路线

深度对比同类 [RepoMCP（Python 版）](https://github.com/areeb1501/RepoMCP)。Python 版走**纯 GitHub REST API** 路线：1915 行单文件、6 依赖（fastapi/fastmcp/pydantic/requests/uvicorn/python-dotenv）、单仓硬编码、Vercel 可部署。本项目坚持**本地 git + BM25**，理由：BM25 质量远高于 GitHub Search（CJK 差、60 次/分钟限流）、可做符号抽取、离线可用、不依赖 API 配额。

### P1：补原始功能 + 轻量扩展（易实现）

| 功能 | 来源 | 实现路径 |
|---|---|---|
| `tools_pulls.go`（list/get/create PR） | 原始项目有、改版丢失 | GitHub REST，与现有 issue.go 同级，约 300 行 |
| `get_diff`（两分支/两 commit） | Python RepoMCP `get_diff` | `git diff branch1..branch2 --stat` |
| `get_commit`（单 commit 详情） | Python RepoMCP `get_commit` | `git show --stat --format=fuller <sha>` |
| `list_branches` | Python RepoMCP `list_branches` | `git branch -a` 本地 |
| `search_file_contents`（正则模式） | Python RepoMCP `search_file_contents` | 已索引文件 + Go `regexp` 匹配，与 BM25 并行召回 |
| MCP prompts 端点（3–5 预定义模板） | Python RepoMCP `show_common_workflows` | `prompts/list` 从空返回 issue 调研流程 / PR 检查清单 / 代码定位流程模板 |

### P2：写能力

| 功能 | 实现路径 | 风险 |
|---|---|---|
| `create_branch` / `delete_branch` | `git branch` + GitHub REST 远端同步 | 删分支不可恢复，需护栏（禁止删 main/master/production） |
| `reset_branch_to_commit` | `git reset --hard` + `push --force-with-lease` | 必须走 feature branch 隔离 |
| `revert_file_to_commit` | `git checkout <sha> -- <path>` | 单行操作 |
| `patch_file`（局部修改） | 接受 unified diff → `git apply patch` | 小模型生成合规 diff 难度大，需护栏（最大行数、不跨文件）。**比 Python 版字符串替换更可靠**（Python 版 `patch_file` 实际是接收 `patches` 列表做 Python 内存替换，然后 PUT 整个文件——容易错、无法跨文件上下文匹配） |

### P3：架构扩展

| 功能 | 说明 |
|---|---|
| Docker + systemd | 取代 Vercel（不适合数据目录场景） |
| Prometheus /metrics | 同步耗时、工具调用计数、issue 创建率、索引大小 |
| CJK bigram 分词 | 替代单字 token，提升中文检索召回 |
| 配置热加载 | SIGHUP 或 `POST /config/reload` |
| 文件 CRUD + PR 全流程 | `create/update/delete/rename` → feature branch → commit → push → `create_pull_request`。**比 Python 版走 contents API PUT 整个文件更高效**（Python 版每次修改必须读旧 SHA → 生成新内容 → PUT，无法做原子 patch） |
| 分支保护规则 | 配置里指定 protected branches，所有写操作自动走 feature branch → PR |

### 明确不做

| 功能 | 原因 |
|---|---|
| GitHub Search API 替代本地索引 | BM25 质量远高于 Search（CJK 差、限流、截断） |
| Vercel / Serverless 部署 | 本地 clone + 数据目录是架构核心 |
| 向量检索 / embedding | 与 BM25 精确匹配相比复杂度换不来收益 |
| 整个文件重写（create_or_update_file） | 走 `patch_file`，局部编辑更安全、省 token |
| 删除端点（delete_branch / delete_file / delete_pr） | 原始项目契约不含删除，本改版保留 |
| Python 路线复刻 | 纯 GitHub REST API 质量不如本地 git 架构；Python 版的 `patch_file` / `search_code` / `create_or_update_file` 在 Go 本地路线下有更优实现 |
