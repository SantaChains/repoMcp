# repoMcp（SantaChains 改版）

给 **IM 机器人 / MCP 客户端** 用的仓库源码检索 MCP 服务：让聊天机器人里的 LLM 能查你的源码、带着可核验的出处回答问题，并在调研清楚之后代用户提交与管理 issue。

> 本项目基于 [zerx-lab/repoMcp](https://github.com/zerx-lab/repoMcp)（MIT 许可）改版。原始项目核心设计（本地 clone + 内存 BM25 索引 + Streamable HTTP MCP）全部保留；改版新增多仓并行同步、浅克隆、结构化日志与 trace-id、配置强校验与脱敏、敏感词过滤、HTTP 超时与优雅关闭、/sync 手动同步端点、双层速率限制等功能。详见底部「原始项目 vs 本改版」章节。

- 传输：**无状态 Streamable HTTP**，单端点 `POST /mcp`，Bearer 鉴权。
- 检索：本地 clone + 内存倒排索引（BM25）+ 正则符号表。**不需要 embedding、向量库或任何外部服务**。
- issue：可选开启，走 GitHub REST。只实现「建 / 评论 / 改状态与标签」，**没有任何删除动作**，且创建前强制查重与限频。
- 依赖：**零第三方 Go 依赖**，`CGO_ENABLED=0` 单二进制。运行期只要求宿主有 `git`。

## 设计取舍

消费方是 IM 里的**小模型**：上下文窄、通常只肯调 1–3 次工具。这与 coding agent 完全不同，因此：

| 决定 | 原因 |
|---|---|
| 一次调用给足证据，不指望多轮迭代 | 小模型不会像 coding agent 那样反复 grep 收敛 |
| 输出是紧凑纯文本，不是 JSON | JSON 包装只增加 token，对模型阅读无收益 |
| 每条结果带 `路径:行号` + 钉住 commit 的 permalink | 答案必须能被人工核验，否则代码问答没有价值 |
| 硬字节预算（`maxResponseBytes`） | 消费方 MCP 客户端**不截断** tool 返回，服务端必须自己收口 |
| 工具按能力动态挂载 | 工具越多小模型选错概率越高；没接入 issue 的部署仍然只有 5 个检索工具 |
| 写操作的护栏落在服务端 | 「先调研再提 issue」「别重复提」「别随手关」写进提示词只是建议，只有服务端硬校验拦得住 |
| 词法 + 符号表，不做向量检索 | 代码检索里标识符精确匹配的召回远超 embedding；向量的复杂度换不来相应收益 |

## 工具

**检索（始终可用）**

| 工具 | 用途 |
|---|---|
| `repo_overview` | 技术栈、规模、目录结构、README 摘要。**让模型先建立坐标再检索** |
| `search_code` | 关键词检索，返回带行号与链接的片段。支持 `repo` / `lang` / `path_glob` 过滤 |
| `read_file` | 按行范围读原文（单次上限 400 行）。`blame: true` 可为每行附加最后修改的提交与作者 |
| `find_symbol` | 按名字查定义，返回签名与文档注释。已知符号名时比 `search_code` 精确 |
| `git_history` | 提交历史，回答「为什么这么写」「什么时候加的」 |

**issue（配置了 `repos[].issues` 才挂载）**

| 工具 | 用途 | 前提 |
|---|---|---|
| `search_issues` | 查已有 issue：回答「有人提过吗 / 现在有哪些待办」，也是创建前的查重手段 | `issues` 已配置 |
| `read_issue` | 读单条 issue 的完整正文与最近讨论 | 同上 |
| `create_issue` | 代用户提交 issue，正文由服务端按模板渲染 | `issues.write: true` |
| `update_issue` | 追加评论、关闭、重开、增删标签 | 同上 |

服务在 `initialize` 时会下发 `instructions`，向模型声明可用仓库、各仓的 issue 能力、工具选择规则，以及**必须引用来源、检索无果时不得编造**。

## 配置

复制 `config.example.json` 为 `config.json`：

```json
{
  "listen": ":8790",
  "token": "长随机串",
  "dataDir": "./data",
  "syncInterval": "15m",
  "maxResponseBytes": 12000,
  "githubToken": "有 issues 权限的 PAT",
  "maxIssueCreatesPerHour": 5,
  "repos": [
    {
      "name": "repo",
      "desc": "SantaChains 主仓（这句话会展示给模型，帮它选对仓库）",
      "url": "https://github.com/SantaChains/repo.git",
      "ref": "main",
      "webBase": "https://github.com/SantaChains/repo",
      "exclude": ["packages/**"],
      "issues": { "write": true }
    }
  ]
}
```

| 字段 | 说明 |
|---|---|
| `token` | Bearer 令牌。留空则**不鉴权**，仅可用于 `127.0.0.1` |
| `dataDir` | 各仓 clone 的存放目录，默认 `./data` |
| `syncInterval` | 后台 fetch + 重建索引周期，`"0"` 关闭 |
| `gitTimeout` | 单条 git 命令超时，默认 `3m` |
| `maxResponseBytes` | 单次工具返回的字节预算，默认 12000 |
| `repos[].name` | 短名，模型用它作 `repo` 参数。限 `^[a-z0-9][a-z0-9._-]{0,63}$` |
| `repos[].desc` | 一句话说明，出现在 `instructions` 与 `repo_overview` 里 |
| `repos[].webBase` | permalink 前缀。留空则从 `url` 推导（会剥掉内嵌凭据） |
| `repos[].include` / `exclude` | 通配符过滤，支持 `*`（不跨 `/`）、`**`（跨 `/`）、`?`；不含 `/` 的模式匹配文件名 |
| `repos[].dir` | 覆盖本地路径。**不要指向你的开发工作树**——同步会 `reset --hard` + `clean -fd` |
| `githubToken` | issue 工具用的 PAT（写操作需 `issues:write`）。留空则 issue 只能读公开仓且限流严格 |
| `githubApiBase` | API 根地址，默认 `https://api.github.com`；GHE 填 `https://<host>/api/v3` |
| `githubTimeout` | 单次 GitHub API 调用超时，默认 `20s` |
| `maxIssueCreatesPerHour` | 单仓每小时创建 issue 的上限，默认 5，`0` 表示不限 |
| `repos[].issues` | 省略 = 该仓无 issue 能力；`{}` = 只读；`{"write": true}` = 可创建与管理 |
| `repos[].issues.slug` | `owner/repo`，留空则从 `webBase` / `url` 推导；推导不出会**启动失败** |
| `repos[].issues.token` | 覆盖全局 `githubToken`（跨组织多 PAT 时用） |
| `repos[].issues.labels` | 允许模型使用的标签白名单。留空则以仓库现有标签为准 |

环境变量可覆盖：`REPOMCP_CONFIG` / `REPOMCP_LISTEN` / `REPOMCP_TOKEN` / `REPOMCP_DATA` / `REPOMCP_GITHUB_TOKEN`。

私有仓：在 `url` 中内嵌 token（如 `https://x-access-token:<PAT>@github.com/owner/repo.git`），或在宿主上预先配好凭据助手。服务已强制 `GIT_TERMINAL_PROMPT=0`，凭据缺失会直接失败而不是挂起等待输入。

## 运行

```bash
cd repoMcp
go run . -config config.json      # 本机
task build                        # 交叉编译 linux-amd64 到 build/repomcp
```

启动参数（均为可选）：

| 参数 | 作用 |
|---|---|
| `-config <path>` | 指定配置文件路径，默认 `config.json` |
| `-version` | 打印标题与版本后立即退出（容器健康探针） |
| `-print-config` | 打印**脱敏**后的最终配置（token/PAT 仅显示首尾几字符）并退出 |
| `-check-config` | 仅校验配置合法性，打印摘要并退出，**不启动服务**——适合在部署流水线里做启动前检查 |

启动后服务立即可用，首次 clone 与索引在后台进行；未就绪时工具会明确回复「索引进行中」而非空结果。

探活：`GET /healthz`（无需鉴权），返回每个仓的 HEAD、文件数、符号数、上次同步时间、错误，以及 issue 能力（`off` / `read` / `write`）。全部仓库就绪前返回 `503`。响应还附带运维字段：`lastSyncDurationMs`、`lastSyncEnd`（UTC 时刻）、`nextSyncInSeconds`（未排期为 `-1`）、`indexedFiles` / `indexedLines` / `indexedSymbols` 合计数，便于接入大盘。

手动触发同步：`POST /sync`（Bearer 鉴权，与 `/mcp` 共用令牌）。Body 可选：

```json
{"blocking": false}
```

- `blocking=false`（默认）：无正在进行的同步即返回 `202 {"status":"scheduled"}`；已在跑则 `202 {"status":"running"}`。
- `blocking=true`：等本轮同步结束后返回 `200`，响应含 `status`、`shared`（与并发请求共享）、`durationMs`、`lastSyncEnd`、`nextSyncInSec`。手动触发后会按 `syncInterval` 重排下一次定时同步。

请求体大小上限 1 MB，超过会收到 `413`。CORS 已开启：`POST, OPTIONS`，允许跨源携带 `Authorization` / `Content-Type` / `MCP-Protocol-Version`，并把 `X-Trace-ID` 暴露给前端。

## 接入 MCP 客户端

### AstrBot（推荐）

打开 AstrBot 管理后台 → 扩展 → MCP（通常是 `http://localhost:6185/#/extension/mcp`）→ 新增服务器，粘贴以下配置：

```json
{
  "name": "repomcp",
  "transport": "streamable_http",
  "url": "http://127.0.0.1:8790/mcp",
  "headers": {
    "Authorization": "Bearer <替换为 config.json 的 token 字段值>"
  },
  "timeout": 60
}
```

| 字段 | 说明 |
|---|---|
| `name` | AstrBot 工具列表里显示的名称，英文唯一，建议 `repomcp` |
| `transport` | **必须写 `streamable_http`**——repoMcp 是无状态 Streamable HTTP，没有 SSE/WebSocket 会话。写其它值会直接失败 |
| `url` | repoMcp 服务的 `/mcp` 端点，端口与 config.json 的 `listen` 一致（默认 `:8790`）。反代场景填 `https://域名/mcp` |
| `headers.Authorization` | `Bearer ` + config.json 的 `token` 字段裸串。**必须带 `Bearer ` 前缀**，只填裸 token 会得到 401 |
| `timeout` | 单次工具调用超时（秒），建议 ≥ 60。大仓首次同步或 `git_history` 深查询可能接近 30–50 秒 |

AstrBot 管理后台地址（6185 端口）**不是** MCP 通信地址——它只是你粘贴配置的 WebUI。repoMcp 必须在本地启动监听 `:8790`，AstrBot 才会通过 streamable_http 调用工具。

### 其它 MCP 客户端

通用客户端（如 Claude Desktop、OpenClaw、Trae 等）配置示例字段名可能不同，但三条必须满足：传输层为 streamable_http、URL 以 `/mcp` 结尾、带 Bearer Authorization 头。

排障顺序：先 `curl http://127.0.0.1:8790/healthz` 看 `ready` 与每仓 `error`（此接口不需要鉴权），再用下面的探针验证 MCP 握手，最后才怀疑客户端配置。

**安全**：源码侧完全只读——不执行仓库中的任何代码，也不接受任意路径读取（`read_file` 只能读已索引的受版本控制文件，并拒绝 `..` 与绝对路径）。唯一的写入面是 issue，且实现上只有「建 issue / 评论 / 改状态与标签」四种动作，没有任何删除端点，最坏后果是多一条可被人工撤销的 issue。但本服务会把私有仓源码送进 LLM——请确认所用模型的数据策略，并把服务绑定在内网或 `127.0.0.1`。

P0 级额外安全基线：

| 机制 | 说明 |
|---|---|
| Bearer 恒定比较 | `Authorization` 与服务端 token 以 `crypto/subtle.ConstantTimeCompare` 比较，防时序侧信道 |
| 请求体大小上限 | `/mcp` 读 body 前使用 `io.LimitReader(1 MB+1)`，超 1 MB 返回 `413`；HTTP `MaxHeaderBytes=1 MB` 防 header flood |
| 结构化 JSON 日志 + `X-Trace-ID` | 每条 JSON-RPC 请求生成（或继承请求头）的 8 字符 trace-id，写入响应头 `X-Trace-ID`，便于跨层排障 |
| 配置文件权限告警 | 非 Windows 平台会检查 `config.json` 是否为 `0600`；权限过松会在启动日志中打印警告 |
| 敏感词过滤 | 所有工具级错误消息在进入最终 JSON-RPC 响应前统一脱敏：GitHub PAT 家族 (`ghp_` / `github_pat_` / `gho_` / `ghs_` / `ghu_`)、Authorization/Bearer/Token 前缀值、以及配置中出现的任何裸 token 都会被替换为 `<REDACTED>` |
| JSON-RPC 错误规范化 | 错误码按 JSON-RPC 2.0（`-32700/-32600..-32603`）返回；错误消息长度封顶 1024 rune；长 ID（字符串或任意精度数字）可直接兼容，非法或空 id 规范为 `null` |
| Panic 恢复 | `/mcp` 及其它端点 panic 被 recoverMiddleware 捕获，分别返回 `-32603 internal error`（JSON-RPC）或 `500 server error`，不会拖垮进程 |
| 优雅关闭 | SIGINT/SIGTERM 触发后先 `http.Shutdown`（30s 宽限）再 `Store.Shutdown()`，等待同步循环干净退出后再终止 |
| git 子进程防护 | 全局并发 git 子进程受 `gitSem`（2–6，等于 CPU 核数夹到范围）限制；单个子进程 stdout/stderr 各自最多 128 MiB，超过立刻报错或截断，防止 OOM |

## GitHub Token 权限说明

`config.json` 里的 `githubToken` 控制 issue 工具能否工作。GitHub 有两层权限模型，**必须同时满足两层才能写入**：

| 层 | 判定主体 | 内容 |
|---|---|---|
| 账号层 | GitHub 账号本身 | 你对目标仓是 owner / collaborator / member，还是外部访客 |
| Token 层 | PAT 的 scope（Classic）或 permissions（fine-grained） | token 被授予了哪些操作权限 |

两层任何一层不满足，GitHub 都会返回 403，错误消息通常是 `Resource not accessible by personal access token`。

### Classic vs fine-grained 选型

| 维度 | Classic（`ghp_` 前缀） | fine-grained（`github_pat_` 前缀） |
|---|---|---|
| 粒度 | scope 粗——勾 `public_repo` 一选 = 所有公开仓完整读写 | 按仓 + 按功能（Issues: Read and write / Contents: Read-only） |
| 他人公开仓写 issue | **可以**——`public_repo` scope 对所有公开仓自动授予 issue 读写，不需要 collaborator 身份 | **不行**——必须是目标仓 collaborator 才能写入，scope 勾全也没用 |
| 撤销/变更 | 只能删除整个 token，无法局部减权 | 可以改 scope（勾多了能减），token 字符串不变 |
| 本项目最小 scope | 写 issue：`public_repo`（仅需公开仓）或 `repo`（含私有仓）<br>只读 issue：不勾任何 scope（匿名读） | 写 issue：Issues Read and write + Contents Read-only（按仓逐个勾）<br>只读 issue：Issues Read-only（按仓或 All public） |
| 适用场景 | 跨多个他人公开仓发 issue；scope 需求简单 | 只给自己拥有的仓；需要精确控制哪些仓、哪些操作 |
| GitHub 推荐 | legacy，不推新 | 官方推荐 |

### 场景 → Token 类型对照

| 场景 | 推荐 token | scope | 备注 |
|---|---|---|---|
| 给自己拥有的仓代发 issue | fine-grained（推荐） | Issues Read and write + Contents Read-only（按仓） | 精确最小权限 |
| 给多个他人公开仓代发 issue | **Classic `public_repo`** | `public_repo` | 实测可行（Classic 对公开仓 issue 写入不要求 collaborator）；fine-grained 在同样条件下会 403 |
| 只读 issue（search_issues / read_issue） | 任意类型甚至不填 | fine-grained: Issues Read-only；Classic: 不勾 scope | 公开仓 issue 匿名可读；填 token 只是为了更高的 API 限流配额 |
| 私有仓 | 必须 Classic `repo` 或 fine-grained 按仓勾 | Classic: `repo`；fine-grained: 对应仓 Issues Write + Contents Read-only | 私有仓必须有 token |

### per-repo token 覆盖

`config.json` 支持 `repos[].issues.token` 字段——可以给每个仓单独指定 PAT，覆盖全局 `githubToken`。当不同仓分属不同组织、需要不同账号的 token 时用这个。

```json
{
  "githubToken": "全局 token（给大多数仓用）",
  "repos": [
    {
      "name": "corp_repo",
      "issues": { "write": true, "token": "该组织专用 PAT" }
    }
  ]
}
```

### 快速验证 token 是否够

配置好后用 curl 验证（替换 token 和仓名）：

```bash
# 读权限（任何 token 都能过）
curl -s -H "Authorization: Bearer <token>" \
  https://api.github.com/repos/<owner>/<repo>/issues

# 写权限（会创建一条 issue，记得立即关闭）
curl -s -X POST -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"title":"[test] ignore","body":"will close"}' \
  https://api.github.com/repos/<owner>/<repo>/issues
```

第二个请求返回 201 = token + 账号两层权限都够；返回 403 = 至少一层不足，看错误消息里是"Resource not accessible"（账号层）还是"Missing scope"（token 层）。

## issue 能力

只有配了 `repos[].issues` 的仓库才会出现 issue 工具，`write` 决定挂不挂 `create_issue` / `update_issue`。

**为什么护栏在服务端**：消费方是 IM 里的小模型，「先调研再提」「别重复提」「别随手关」写进工具描述只是建议。凡是能硬校验的都在服务端拦：

| 护栏 | 行为 |
|---|---|
| 强制查重 | 创建前服务端自己再查一遍，命中疑似重复直接拒绝并列出候选；模型只能在逐条核对后带 `confirm_not_duplicate=true` 重试 |
| 双路召回 | 搜索接口覆盖历史 issue，最近 open 列表兜底中文标题（GitHub 搜索对 CJK 分词很差）。标题按重叠系数打分，阈值 0.55 |
| 双层速率限制 | 单仓每小时 `maxIssueCreatesPerHour` + 全局**60/小时**（防 flood）+ 同 reporter（FNV-1a 哈希桶）**10/小时**。配额在调用 GitHub 之前扣除，失败重试照扣。三种限流**同时生效**，以最严格者为准 |
| 输入长度封顶 | title 6–200、body 20–8000、evidence 10–4000、comment 10–4000、repro ≤2000、env ≤1000、reporter ≤60，单次 add/remove 标签 ≤20，单 label ≤40 rune |
| 必填调研结论 | `confidence` 只能是 `confirmed` / `unconfirmed`；`confirmed` 时 `evidence` 里没有 `路径:行号` 形式的出处直接拒绝 |
| 正文服务端渲染 | 模型只能填各段内容，结构固定：现象 → 调研结论（带确定性标注）→ 复现 → 环境 → 提交来源 |
| 标签白名单 | GitHub 打标签会顺手新建不存在的标签；只有仓库现有（或配置白名单里的）标签会被采用，其余忽略并在结果里说明 |
| 状态合法性 | `close` 必须 `comment`（≥10 字）+ `reason ∈ {completed, not_planned}`；`reopen` 必须 `comment` 且**不能**提供 `reason`；重复 close/open 会被拒绝；二次校验确保 `State × StateReason` 组合符合 GitHub REST 语义 |
| 仓库必须对得上 | 有多个可写仓时 `repo` 不可省略；对未接入 issue 的仓库调用会明确说明原因，而不是退而求其次挑一个 |

机器人提交的 issue 与评论都带来源标注（提交人 + `repoMcp` + 索引 commit），维护者可一眼分辨并追溯。

## 检索语义

两条规则决定了结果质量，值得知道：

**覆盖率门槛。** 索引会把 `max_retry_count` 拆成 `max`/`retry`/`count`，这是精确查询能命中的关键；
代价是任何一个常见子词都可能把无关文件拉进结果。因此要求：查询中的某个原始词必须
**整词命中**，或**其全部子词同时命中**。所以 `parseHTTPResponse` 能找到 `parse_http_response`，
而 `zzqqxx_not_exist_token` 不会仅因为文件里出现 `token` 就返回一堆无关代码——它返回零结果。
多词查询里覆盖词数越多的结果排得越前。

**跨命名风格。** `find_symbol` 会把标识符归一为「去分隔符的小写子词串」再比较，
`aria2StatusStr` 因此能找到 `aria2_status_str`。模型跨语言时经常记错命名风格
（Dart camelCase / Rust snake_case），查定义的首选工具不能对此敏感。

## 验证接入

`mcp_probe.py` 用 **MCP 标准 Python SDK** 走一遍完整流程
（initialize → tools/list → tools/call → ping），可在配置客户端前先确认服务可用：

```bash
uv run --with mcp --with httpx --with anyio python mcp_probe.py
```

默认打本地。打线上用环境变量覆盖：

```bash
REPOMCP_PROBE_URL=https://你的域名/mcp REPOMCP_PROBE_TOKEN=你的token \
  uv run --with mcp --with httpx --with anyio python mcp_probe.py
```

## 局限

- 符号提取是**语言感知的正则启发式**，不是完整语法分析。选它是为了保住零依赖与 `CGO_ENABLED=0` 交叉编译；代价是复杂泛型签名、宏生成的定义可能漏抽。`search_code` 的全文检索不受此影响。
- 索引常驻内存，随仓库规模线性增长。百万行级仓库请留意进程内存。
- clone 使用 `--depth 1` 首次 + `--depth 50` 增量 fetch。`git_history` 默认展示最近 50 层，不够时可在工具里传 `limit` 拉大。

---

## 原始项目 vs 本改版

| 维度 | 原始 [zerx-lab/repoMcp](https://github.com/zerx-lab/repoMcp) | SantaChains 改版 |
|---|---|---|
| 基础架构 | Go 本地 clone + BM25 + Streamable HTTP | 相同，核心设计保留 |
| 多仓同步 | 串行逐仓 Load | goroutine 并行，耗时 ≈ max(各仓) |
| git clone | `--depth 200` | `--depth 1 --no-tags` 首次，`--depth 50` 增量 fetch |
| 指令词缓存 | 每次 initialize 重新拼接 | 启动 + 每次同步后重建并缓存，initialize 零分配 |
| 工具数量 | 5 检索 + 4 issue + PR（tools_pulls.go） | 5 检索 + 4 issue（PR 工具**暂未恢复**，见未来路线） |
| 日志 | 标准 log 包 | 结构化 JSON + trace-id |
| 配置校验 | 弱校验 | 11 项强校验 + -check-config / -print-config 预检查 |
| 敏感词过滤 | 无 | SanitizeError 正则脱敏（7 类 GitHub PAT + 自定义 token） |
| HTTP 防护 | 无显式超时/大小上限 | ReadHeaderTimeout/ReadTimeout/WriteTimeout/IdleTimeout + MaxHeaderBytes + 1 MB body 上限 |
| 优雅关闭 | 无 | SIGINT/SIGTERM → http.Shutdown → Store.Shutdown |
| /sync 端点 | 无 | POST /sync 手动触发，blocking 同步模式，singleflight 防重复 |
| issue 限频 | 单仓每小时 | 单仓 + 全局 + reporter 哈希桶 **三层同时生效** |
| LICENSE | 无 | MIT（原始作者 zerx-lab + 改版作者 SantaChains 双版权） |

## 未来路线审计

参考同类项目 [RepoMCP（Python 版，纯 GitHub API）](https://github.com/areeb1501/RepoMCP) 的能力清单，以下是基于本项目架构（本地 clone + git 子进程 + BM25 内存索引）的可借鉴功能筛选。**不适合借鉴的是纯 API 路线（如 Vercel 无状态部署、整个文件重写）——我们的本地索引架构质量更高，必须坚持。**

### P1：容易实现，价值高（补回原始功能 + 轻量扩展）

| 功能 | 来源 | 实现路径 | 价值 |
|---|---|---|---|
| 补回 `tools_pulls.go`（list/get/create PR） | 原始项目有、改版时丢失 | GitHub REST API，和现有 issue.go 同级，约 300 行 | PR 管理是原始项目已有能力，不应丢失 |
| `get_diff`（两分支/两 commit 间 diff） | Python RepoMCP 有 | `git diff branch1..branch2 --stat` + 可选带行号片段 | 回答"这次改了什么"是高频提问，当前 git_history 不够 |
| `get_commit`（单 commit 详情） | Python RepoMCP 有 | `git show --stat --format=fuller <sha>` | git_history 只能列，看不到单 commit 全貌 |
| `list_branches`（分支列表） | Python RepoMCP 有 | `git branch -a` 本地已有，或 GitHub REST 远端 | 让模型知道有哪些分支可以对比 |
| `search_file_contents`（正则模式搜索） | Python RepoMCP 有 | 对已索引文件用 Go `regexp` 做匹配，和 BM25 并行召回 | BM25 查"这个类有没有被谁实例化"很弱，正则补漏 |
| `show_common_workflows`（MCP prompts 端点） | Python RepoMCP 有 | `prompts/list` 返回空 → 改为返回 3-5 个预定义模板（issue 调研流程、PR 检查清单、代码定位流程） | 小模型从 prompts 端点拉模板比自己组织更稳 |

### P2：中等难度，需新增写能力

| 功能 | 实现路径 | 风险 |
|---|---|---|
| `create_branch` / `delete_branch` | `git branch` 命令 + GitHub REST 远端同步 | 分支删除不可恢复，需服务端护栏（禁止删 main/master/production） |
| `reset_branch_to_commit` | `git reset --hard <sha>` + push --force-with-lease | 写操作，必须走 feature branch 隔离 |
| `revert_file_to_commit` | `git checkout <sha> -- <path>` | 单行操作，风险可控 |
| `patch_file`（局部修改文件） | 接受 unified diff → `git apply patch` | 小模型生成合规 diff 有难度，需要护栏（最大 diff 行数、不允许跨文件） |
| 标签白名单配置增强 | 当前已有，扩到 issue + PR 统一 | 低 |

### P3：架构扩展（长周期）

| 功能 | 说明 | 前提 |
|---|---|---|
| Docker 镜像 + systemd 服务文件 | 取代 Vercel 无状态部署（不适合数据目录场景） | 容器化需求明确 |
| Prometheus 指标暴露 | /metrics 端点：同步耗时、工具调用计数、issue 创建率、索引大小 | 运维大盘接入 |
| CJK 分词优化 | 当前 BM25 对中文分词差，可加 bigram 拆分替代单字 token | 中文检索成为主场景 |
| 配置热加载 | SIGHUP 或 `POST /config/reload` 刷新 repos 数组、新增仓触发异步 clone | 运维频繁增删仓 |
| 文件 CRUD + PR 全流程 | `create_or_update_file` / `delete_file` / `rename_or_move_file` → 创建 feature branch → commit → push → create_pull_request | 从"只读检索 + issue 代提"扩展到"代码编辑 + PR" |
| 分支保护规则 | 配置里指定 protected branches（main/master/production），所有写操作自动走 feature branch → PR 流程 | 代码编辑能力落地后才有意义 |

### 明确不做

| 功能 | 原因 |
|---|---|
| GitHub Search API 替代本地索引 | BM25 质量远高于 GitHub Search（CJK 差、60 次/分钟限流、结果截断） |
| Vercel / Serverless 部署 | 本地 clone + 数据目录是架构核心，无状态 Serverless 不适合 |
| 向量检索 / embedding | 与 BM25 精确匹配相比复杂度换不来质量收益 |
| 整个文件重写（create_or_update_file） | 走 patch_file 路线，局部编辑更安全、更省 token |
| 任何删除端点（delete_branch / delete_file / delete_pr） | 原始项目设计就"契约刻意不含任何删除操作"，本改版保留此原则 |

### 参考对比

- **GitHub REST 路线 vs 本地 git 路线**：Python RepoMCP（纯 API）的优势是零依赖、无本地存储、可 Serverless 部署；劣势是搜索弱、限流紧、无法做符号抽取和 BM25。本项目坚持本地 git + BM25，因为消费方是 IM 小模型，检索质量比部署方便重要。
- **Python RepoMCP 的 `patch_file`** 设计思路值得借鉴：让模型只生成目标片段的 unified diff，而不是整个文件重写。我们用本地 `git apply` 比它走 GitHub REST contents API 更高效。
- **Python RepoMCP 的分支保护** 强制所有写操作走 feature branch + PR 流程。这个安全护栏思路优秀，等 P3 代码编辑能力落地后必须跟上。
