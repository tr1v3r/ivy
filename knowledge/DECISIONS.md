# ivy 架构与选型说明

格式：证据 A = 代码现状/提交可回查；C = 推测（待维护者确认）。
统一日期 2026-10-12，HEAD = 1689bbf（全条目对照代码复核）。与 ARCHITECTURE.md
§7 分工：那边写「代码结构自明」的取舍，这边写「仍生效的契约」。

## D-001：内容统一 []byte，格式逻辑全部下沉 driver

选择：处理器链统一操作 `[]byte`，引擎核心对格式零感知；每种格式实现
Driver = PathParser + Realizer + Modem。牺牲类型安全，换核心稳定与新格式零
侵入。证据：434ab5b、8e60da4、5932981、d2f744e。重新评估条件：出现需要
跨格式强类型查询的场景。状态：生效中。

## D-002：RealizeContext 贯穿 realize 链，Get 与 GetWithContext 双轨

选择：`Process(rc, before)` 全链携带 rc；`Get/GetVal` 传 nil 由 `t.defaultCtx`
兜底；动态参数走 `GetWithContext`。rc 内嵌 `context.Context`。
证据：064ec05、9511114、4390f6e。本轮复核：TreePath/ParentContent 已由下钻
路径赋值（a208275/#17，T-001/T-003 闭环）；rc.Context 由 web 层填请求
Context 并透传 CURL（#83）。重新评估条件：context 需求消失或改 per-request
处理器实例化。状态：生效中。

## D-003：不导出 RealizeContext 的 context.Context 访问器

背景：a0e9e40 添加 `Ctx()` 访问器当天被 79487ad 回退，未写原因。
选择：保持内嵌导出字段（方法集提升），不提供访问器。
动机（C：推测，待确认）：内嵌字段已导出，访问器属冗余 API 面。
重新评估条件：需抽象 Context 类型或做 nil 防护时再议。
状态：生效中（1689bbf 复核：仍无访问器）。

## D-004：CURL 处理器不消费 rc.Params，参数注入留给上层

背景：064ec05 曾让 CURL 拼 rc.Params 进 URL query，d016ff0（#15）移除。
选择（当时）：CURL 只用 op.URL，Params 供用户自写 RawProcessor 闭包消费。
现状：CURL 仍不消费 Params（事实不变），但「上层」已由引擎一等问题
取代——TemplateProcessor + 动态层提供缓存安全的参数化（见 D-009）。
失效点：其权衡「开箱即用的动态内容能力消失」不再成立；URL 级
${param} 模板化（而非内容级）仍不支持。
状态：已失效（由 D-009 取代；失效记录保留，勿据此阻止内容参数化改动）。

## D-005：fallback 仅对「路径解析失败」触发，命中 leaf 不触发

选择：`q.level()==t.level` 直接返回内容，短路 fallback；fallback 只在
「无匹配子节点」时以当前节点内容为输入执行。
证据：d3a14d4（实现+断言翻转）、engine_test.go TestTree_Fallback。
1689bbf 复核：下钻命中层仍短路，契约不变。
重新评估条件：出现「所有返回值统一后处理」需求时另设 hook。
状态：生效中（行为契约，改它就是 breaking change）。

## D-006：限流作用于 realize 慢路径，且仅 lazy/instant/cache 模式

选择：`t.allow()` 在写锁临界区内检查；标准模式 build 期 realize 不限流；
forest 级 allowGet 在 GetVal 入口兜底；超限返回 ErrRateLimited。
证据：0c48939、47902a4（commit 写明「与被保护操作同址」）。
动机（C）：标准模式不限流推测为 build 属一次性受控阶段，待确认。
状态：生效中（realizeLocked 分支复核一致）。

## D-007：CURL 失败返回结构化 CURLProcessError，Error() 脱敏

选择：失败统一返回 `*CURLProcessError`；`Error()` 打码敏感头/URL 凭据/
敏感 query 并 4KB 截断；`Unwrap` 支持 errors.Is/As；结构化字段保留原值。
证据：#16（41595a5）、curl_test.go。
重新评估条件：脱敏名单不覆盖新场景时扩充 isSensitiveHTTPName。
状态：生效中。安全姿态（TLS/体积/白名单）另立 D-010。

## D-008：Processor 可序列化（Modem），驱动 rules 配置

选择：每 Processor 实现 Load/Save；GeneralModem[T] 泛型编解码，按 type
字符串实例化（json/yaml/curl/xml/toml/template）。
约束：RawProcessor/CombinedProcessor 不支持序列化；serve.go load() 的类型
switch 是注册点；未知 type 与 Load 失败的 processor 连同整条 directive
一并丢弃并报错（#60/#88——旧版变 nil 槽位被静默跳过）；文件级失败
fail-loud（#58/#85）。重新评估条件：类型增多时改注册表模式。
状态：生效中。

## D-009：ParamAware 动态层——链在首个参数感知处理器处切分（#29）

选择：实现 `ParamKeys()` 即 ParamAware；`dynamicSplit` 把链切成
静态前缀（照旧 realize 入缓存）+ 动态层（仅目标节点每查询叠加，结果
只返回调用方、绝不写缓存）。TemplateProcessor（${key} 插值，Pattern 空=
就地插值传入内容）是内置实现；IsParamAware 穿透 CombinedProcessor（#67）。
权衡：动态层每查询重放（性能）换缓存与参数无关（正确性）；Get() 返回
静态基座、占位符原样保留；中间节点动态层不执行。
证据：458d6b1（#29）、00763e6（#67）、param_test.go 契约测试、
AGENTS.md「Param-Aware Processors」节。
重新评估条件：需要 URL 级模板化（如 CURL URL ${param}）时扩展同机制。
状态：生效中。取代 D-004。

## D-010：CURL 安全姿态由引擎自有（#87/#83）

选择：自有 verifying/insecure 两个 http.Client（不复用进程级
fetch.DefaultClient，防他库改全局 TLS 行为）；默认校验 TLS（≥TLS1.2），
`insecure:true` 逐规则显式跳过；响应体默认 10MiB 上限（MaxBytes 正值
覆盖、负值关闭）；`IVY_CURL_ALLOW_HOSTS` 主机白名单且重定向目标同受
校验；scheme 限 http/https；rc.Context 取消透传到出站请求。
权衡：开箱默认收紧（自签上游需显式 insecure）；白名单是进程级环境变量。
证据：964a17c（#87）、497ce25（#83）、curl_optin_test.go。
重新评估条件：需要 per-rule 白名单或非 HTTP scheme 时扩展。
状态：生效中。

## D-011：重算恒从 base 起 + 新鲜度 = min(自身TTL, 父发布版本)

选择：节点 realize 永远以 base（根模板/建树时父快照/lazy 下钻携带的
父内容）为输入，绝不接自身前次输出——instant/TTL 重算对非幂等链不
叠乘（#63）；contentVer/realizedFrom 让子节点察觉父内容已刷新而自身
缓存过期（#48/#80）；lazy 继承决策收进写锁临界区（#71）；Set/apply 与
所有链读取同锁（#47/#79）。权衡：全链重放 + 版本比较复杂化。
证据：544f11f、0f69191、a78ad9c、1df0a02，stress_test.go 并发套件。
重新评估条件：引入增量 realize 时重审。
状态：生效中（并发不变式，改动须跑 -race 与 stress 套件）。

## D-012：配置变更走 SIGHUP 显式重载，内容新鲜度走 RULES_TTL（#70/#75）

选择：移除 5s 全量重建 ticker（重放 processors 且与查询竞争）；SIGHUP
重载 RULES_FILE 并整体换 forest（forestMu 保护，失败保留旧 forest）；
RULES_TTL>0 时默认树改 lazy+TTL，由流量驱动重算。
权衡：无自动配置热更——改规则需显式信号或重启。
证据：604ba35（#70）、0aa81cb（#75）、serve.go main 注释（audit W3）。
重新评估条件：需要多实例配置同步时另设机制，勿回退到定时全量重建。
状态：生效中。
