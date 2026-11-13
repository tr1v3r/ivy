# ivy 执行流程（组件边界 = 整库）

组件职责：分层内容构造引擎。输入 = 模板内容 + directives（path + processors）+
可选请求参数，输出 = 按路径寻址、经处理器链实现的 `[]byte` 内容。
组成：forest（树集合）→ tree（路径层级节点）→ directive → driver
（Realizer / PathParser / SegmentParser / Modem / Processor / ParamAware）。
证据：代码现状（2026-10-12 复核，HEAD 1689bbf）。接口词典与模块地图见
ARCHITECTURE.md §1–§3（A 级，勿在此重复）；本图只画运行时控制流。

```text
【构建阶段 build（factory.go / tree.go）】
NewTree/NewLazy*Tree ── byLevel 按路径深度排序 directives
  └─ tree.Set 逐级下钻（getChild：无则 newSubTree+Graft，原子继承
        defaultCtx/fallback（issue #47），base/content 取父 (内容,版本) 快照）
        ├─ 深度匹配 → apply(procs)（realizeMu 写锁内一个临界区，issue #47）
        │    ├─ dynamicSplit：首个 ParamAware 处理器处切链（D-009）
        │    ├─ realizedAt 清零（Set 换链即失效缓存，issue #66）
        │    ├─ 标准模式 → 立即 realize 静态前缀（build 期定型，无限流）
        │    └─ lazy 系模式 → 仅存链，不算内容
        └─ 深度不匹配 → 递归子节点 Set

【查询阶段 Get（tree.go getWithParent；#78 pathQuery 每次下钻只解析一次路径）】
Get(path) → getWithParent(nil 父内容, 0, q)
  ├─ typed-nil 树 → ErrNotExistsTree（errors.go）
  ├─ realizeWithParent → realizeWithParentCtx（rc=nil 回落 defaultCtx，原子槽）
  │    ├─ 快路径 RLock：fresh(parentVer) 即非 instant、TTL 未过、
  │    │    parentVer ≤ realizedFrom（新鲜度 = min(自身TTL, 父发布版本)，#48）
  │    ├─ 慢路径 Lock + double-check（12cbff0）
  │    │    ├─ lazy/instant/cache 模式且限流器拒绝 → ErrRateLimited（D-006）
  │    │    ├─ realizeLocked：恒从 base 起算（绝不接前次输出，#63 防叠乘）；
  │    │    │    lazy 且带父内容 → base = 父内容（继承决策在临界区内，#71）
  │    │    │    静态前缀 procs[:dynamicFrom] 顺序执行；任一 proc 出错
  │    │    │    → 包装上抛，缓存不更新
  │    │    └─ 成功 → set(内容) + contentVer++（发布）+ realizedFrom=parentVer
  │    └─ instant：fresh() 恒 false，每次访问强制重算
  ├─ q.level() == t.level（命中目标节点）→ 返回 t.get() ──★ 不走 fallback（D-005）
  ├─ pickChild(下一段) 命中 → 携带 (content, ver) 递归 getWithParent
  └─ 无匹配子节点 → doFallback：fallback 原子槽 nil → 原样返回；否则
        fallback.Process(nil, 当前内容)（SetFallback 递归传播；nil 入参=清除）

【GetWithContext（tree.go getContextWithParent + dynamicRealize；#29）】
下钻与 Get 完全同一 realize 路径（静态前缀照常缓存），差异三处：
  ├─ 每个途经节点：rc.TreePath = t.path（a208275 恢复，T-001 已解）
  ├─ 途经节点下钻前：rc.ParentContent = 当前 (content, ver) 读（T-003 已解）；
  │    中间节点的动态层不执行，下钻用其静态内容
  └─ 命中目标节点 → dynamicRealize(rc)：动态层 procs[dynamicFrom:] 叠加在
        缓存静态基座上，结果只返回调用方、绝不写回节点（缓存与参数无关）
        Get()（无 rc）→ 返回静态基座，${param} 占位符原样保留

【上下文流 RealizeContext（driver/export.go）】
web 层 GetRule：query 参数（除 name/path）→ rc.Params；请求 Context → rc.Context
  （CURL 透传取消，#83）；未知树名 → 404（#64，不再 panic）
Get/GetVal → rc = nil → realize 内回落 t.defaultCtx（原子槽，#47）
newSubTree 经原子槽拷贝父 defaultCtx/fallback；Set* 系才递归刷既有子树

【四种树模式 × realize 时机（factory.go 入口；字段开关见 tree.go 头注）】
标准   ：build 时 realize 静态前缀一次，Get 快路径永远命中缓存
lazy   ：首次 Get 时 realize，之后永久缓存（TTL=0）
cache  ：lazy + TTL + 父版本门控，min(自身TTL, 父发布版本) 过期即重算（#48）
instant：每次 Get 强制重算（从 base，不叠乘，#63）

【forest / web 进程层（forest.go / web/server.go）】
GetVal / GetValWithContext
  ├─ 树名不存在 → ErrNotExistsTree（安全路径）
  ├─ forest 级限流拒绝 → ErrRateLimited
  └─ 委托 tree.Get / GetWithContext
f.Get(未知名) 仍返回 nil 接口，直接 .Get(path) 会 panic（API 未变，见 TODO T-002R）
builder 经 recover 包装：panic → 记日志、该树跳过（bbdb0ee，S-010 有测试）
web 全局 forest 挂 forestMu（#68）；DefaultBuilder 构建失败记日志不 panic（#72）
serve 生命周期（#70/#75）：无后台定时重建；SIGHUP 重载 RULES_FILE（失败保留旧
  forest）；RULES_TTL>0 时默认树改 lazy+TTL（DefaultCacheBuilder，流量驱动重算）

【CURL 处理器（driver/curl.go，#83/#87 后）】
Process：Method 空默认 GET；rc.Context 透传到出站请求（取消即中止）
  ├─ 前置 checkURL：scheme 限 http/https + IVY_CURL_ALLOW_HOSTS 主机白名单
  │    （未设=放行所有；重定向目标同受校验）
  ├─ 传输/构造/超限失败 → CURLProcessError（无 StatusCode，带 Duration）
  ├─ 非 2xx            → CURLProcessError（含 StatusCode/响应头/响应体）
  └─ 2xx               → 返回响应体（超 MaxBytes 上限报错；默认 10MiB，
        负值显式关闭；Insecure=true 逐规则跳过 TLS 校验，默认校验）
客户端：ivy 自有 verifying/insecure 两个 client，不复用 fetch.DefaultClient
Error()：脱敏（authorization/cookie/token… 头、URL 密码与敏感 query）+ 截断
        （头/体各 4KB）；结构化字段保留原值供 errors.As 显式检查（D-007）
```

维护提示：控制流变化（模式语义、fallback、动态层切分、新鲜度规则）须同步本图；
纯内部重构不用改。图与代码不一致时以代码为准、先核实再动。
