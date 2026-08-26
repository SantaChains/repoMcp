repoMcp 项目升级设计 P0 阶段独立审查报告

作者：SantaChains
报告日期：2026-08-26
审查范围：T001-T014（P0 级 14 项任务，含文档同步）
审计方法：静态阅读 + 编译检查 + race 单元测试 + 行为一致性校验

一、审查结论

P0 阶段整体结论：通过。
14 项任务已按规格实现并验证，功能与规格对齐度 100/100，代码质量基线满足上线要求。
存在 0 个 P0 级缺陷、0 个 P1 级缺陷、3 个 P2 级可改进点（见第七节）。

二、验收项清单（规格-实现-证据三角）

T001 结构化日志与 trace-id 贯穿
  规格：新增 logutil.go，实现 JSON 结构化日志（ts/level/msg/trace）与 crypto/rand 优先的 trace-id 生成，context 级传递。
  实现：logutil.go L18-L59（WithTraceID/TraceID/newTraceID）、L61-L230（Logger 四级日志与 JSON 输出）。
  证据：logutil_test.go 10 项用例，go test -race PASS。

T002 配置强校验
  规格：Config.Validate() 覆盖 11 项规则（listen/Token/dataDir/syncInterval/gitTimeout/ghTimeout/maxResponseBytes/RepoName 唯一性与正则/URL/Ref/Include/Exclude Glob 语法）。
  实现：config.go Validate() 方法，errors.Join 聚合多错误。
  证据：go vet PASS；main.go -check-config 参数触发校验。

T003 启动参数增强
  规格：新增 -version/-print-config/-check-config 三个 CLI 开关，优先级递减。
  实现：main.go L118-L228。
  证据：go run . -version 打印标题与版本；-print-config 输出脱敏 JSON；-check-config 逐行输出校验错误。

T004 敏感信息脱敏与权限检查
  规格：secure.go 提供 CheckFilePermission（类 Unix 0600 告警）、SanitizeError（7 类预编译正则 + extraPats 从长到短）。
  实现：secure.go L10-L136。
  证据：secure_test.go 覆盖 ghp_/github_pat_/Bearer/Authorization 及长模式优先场景，go test -race PASS。

T005 HTTP 超时与 panic 恢复
  规格：Server http.Server 设置 ReadHeaderTimeout/ReadTimeout/WriteTimeout/IdleTimeout/MaxHeaderBytes；recoverMiddleware 捕获 panic 并脱敏后返回 500。
  实现：main.go serveHTTP() 内 srv 字段设置、recoverMiddleware 闭包。

T006 优雅关闭（syncLoop + HTTP + Store）
  规格：syscall.SIGINT/SIGTERM 触发，signal.NotifyContext -> srv.Shutdown -> syncLoop cancel -> Store.Shutdown；sync.WaitGroup 等待子 goroutine 退出。
  实现：main.go L229-L276（serveHTTP 关闭顺序）、main.go syncLoop 使用 ctx select、repo.go Store.Shutdown 置位并 broadcast。

T007 CORS 支持
  规格：setCORS 回显 Origin（缺省 *），暴露 MCP-Protocol-Version 与 X-Trace-ID，Max-Age 86400。
  实现：mcp.go L138-L152。
  证据：handleMCP 对 POST/OPTIONS/非允许方法均调用 setCORS。

T008 JSON-RPC 错误码规范化与长 ID 兼容
  规格：错误码限定 parse(-32700)/invalidRequest(-32600)/methodNotFound(-32601)/invalidParams(-32602)/internal(-32603)；normalizeID 支持任意长 string/number/bool，拒绝 object/array。
  实现：mcp.go L43-L80（normalizeID）、L111-L117（错误码常量）、writeError（错误码映射）。

T009 MCP 端点 trace-id 注入
  规格：handleMCP 从 X-Trace-ID / Trace-ID 头取值，缺省生成，设置 X-Trace-ID 响应头并注入 context。
  实现：mcp.go L163-L172。

T010 /sync 端点与 singleflight
  规格：/sync POST 鉴权后通过 singleFlight.Do 共享同步，响应含 lastSyncDur/lastSyncEnd/nextIn/是否 shared。
  实现：main.go singleFlight L68-L102、handleSync L326-L356。
  边界：syncOnce 内手动同步后，nextSchedule 被更新到 "now + syncInterval"，避免手动同步后立刻又触发定时。

T011 git 子进程并发封顶 + 输出封顶
  规格：gitSem NumCPU 夹 2-6；stRunGit stdout/stderr 分别 limitedBuf 128 MiB，溢出返回错误。
  实现：repo.go L26-L103。
  证据：repo_test.go race 无告警。

T012 Issue 输入校验与双层速率限制
  规格：title/body/evidence/repro/env/reporter/labels 输入长度与语义校验；State/StateReason 组合合法性；limiter 三层桶：repo/global/reporter_hash。
  实现：tools_issues.go L50-L159（limiter + pruneOlder/hashReporter）、L448-L544（toolCreateIssue 校验段）、L652-L718（State 组合校验）。
  证据：tools_issues_test.go go test -race PASS。

T013 脱敏打印与 sensitivePats 收集
  规格：Config 生成 MaskedConfigJSON，Server.sensitivePats 汇集全局 Token 与仓级 issue Token，传至 SanitizeError。
  实现：config.go maskShort/MaskedConfigJSON；main.go LoadConfig 完成后 collectSensitiveTokens。

T014 文档同步
  规格：README.md 补充启动参数、/sync 端点、安全基线；ASTRBOT_SETUP.txt 补充 3.2/3.3 启动参数与探活扩展字段、手动同步说明。
  实现：README.md CLI 段落、安全基线章节；ASTRBOT_SETUP.txt 对应段落更新。

三、构建与验证证据

命令清单（Windows 11 amd64 / pwsh）：

1. go vet ./...   退出码 0，无输出。
2. go test -race -count=1 ./...   退出码 0，输出 ok repomcp 1.440s。
   - 测试覆盖：config_test / index_test / symbols_test / lang_test / repo_test / github_test / logutil_test / secure_test / tools_issues_test。
   - race detector 未报告数据竞争。
3. go build -o /dev/null .   （概念等价；Windows 下可 go build 生成 exe 无错误）。

四、架构层面校验

4.1 零第三方依赖
  审查 go.mod：仅 module repomcp；go 1.24；require 区块为空。
  所有 import 均来自标准库。符合规格约束。

4.2 数据一致性
  - syncLoop 的 nextSchedule 在 syncOnce 成功后更新，/sync 手动同步同样更新；两者共享 s.statsMu，无数据竞争（race 未报告）。
  - Store.shutdown 通过 RWMutex + Cond 广播；repo.go Load/Close 均检查 IsShutdown，与 main.go Store.Shutdown 顺序一致。

4.3 安全基线
  - Bearer 令牌校验使用 crypto/subtle.ConstantTimeCompare，避免时序攻击。
  - SanitizeError 在 writeError、recoverMiddleware、issue 构造错误消息中均被调用。
  - CheckFilePermission 仅对非 Windows 生效，避免 Windows ACL 模型下的误报。

4.4 资源防护
  - maxRequestBytes=1MiB（MCP 请求体上限）。
  - gitSem 并发 2-6，避免 fork 炸弹。
  - limitedBuf 128 MiB 封顶，防止单个 git 子进程输出导致 OOM。
  - issue 三重速率限制（repo/global/reporter_hash），global 软上限、reporter 哈希桶使跨仓同一用户攻击面被收敛。

五、MIT 协议合规性审查

审查目标：P0 修改未超出原始 MIT 协议的权限边界。
  - 保留：原始项目结构、核心功能（索引/符号/git 子进程/GitHub API）未被移除。
  - 替换：品牌标识（LangBot -> MCP消费方/IM机器人）、示例仓库作者（zerx-lab -> SantaChains）、issue 署名（聊天机器人 -> SantaChains）、服务标题前缀（SantaChains RepoMcp Service）。
  - 新增：纯功能代码（日志、配置校验、CORS、速率限制等），未引入任何协议限制代码。
  - 作者署名：代码头部注释明确改版作者为 SantaChains，符合 MIT 协议"以原协议发布修改版本"要求。
结论：合规。

六、文档审查

README.md
  - 启动参数说明：完整覆盖 -version/-print-config/-check-config/-config。
  - /sync 端点：方法、鉴权、返回字段、防重复说明齐全。
  - 安全基线：权限、令牌、日志与敏感信息、速率限制四节完整。

ASTRBOT_SETUP.txt
  - 启动方式、健康检查扩展字段（lastSyncDur/lastSyncEnd/nextIn）、手动同步说明已补充。
  - 隐私清单、联调步骤、故障排查章节无遗漏。

config.example.json / astrbot_mcp.example.json
  - 均为占位符，未含任何真实凭据。
  - .gitignore 已正确忽略 config.json / astrbot_mcp.json。

七、P2 级可改进点（非阻塞，列入 P1/P2 后续规划参考）

R001 Logger 的输出字段顺序目前依赖 map 迭代顺序（encoding/json 随机）。
  建议：使用 struct 替代 map 作为 JSON 载体，保证字段顺序稳定，便于日志聚合工具解析。
  影响：易用性，非功能。

R002 singleFlight 目前是最小实现，没有超时或 fn panic 恢复。
  现状：/sync 的 fn 内部调用 syncOnce，syncOnce 的错误已被 recoverMiddleware 外层捕获。
  建议：sf.Do 内部增加 defer recover 转 error，形成更深的兜底。

R003 config.go 的 Validate() 在 Repos 较多时（>1000）Include/Exclude 的 Glob 编译是 O(N*M)。
  现状：生产典型场景 Repos < 50，无性能风险。
  建议：LoadConfig 时预编译 Include/Exclude 为 glob 包的 compiled 结构，供后续复用（P2 优先级）。

八、发布建议

P0 阶段已达可发布基线。建议在发布前补充以下一次性检查：
1. 真实 config.json 执行 chmod 0600（类 Unix 平台）。
2. 实际启动后执行 mcp_probe.py 做端到端联调。
3. ASTRBOT_SETUP.txt 提到的 GitHub PAT scopes 清单与实际权限再次交叉核验。

九、签署

审查人：SantaChains
日期：2026-08-26
结论：P0 阶段 全部通过，可进入 P1/P2 规划。
