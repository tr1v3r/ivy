# ivy 自然语言测试案例库

执行规范：结果三态——已执行且通过 | 已执行但失败 | 环境不满足未执行；
不得只读代码后宣称「逻辑上应通过」。日期 2026-10-12，HEAD = 1689bbf
（上轮三个〔基线红〕已全部转绿，见 HISTORY H-009）。
标注〔已固化〕= 已有自动化测试。测试清单与分层策略见 ARCHITECTURE.md §6。

## S-001：标准树 build 后查询叶子

前置：NewTree + JSON 驱动，模板 `{"id":1}`，directives 在 `/a/b` create。
动作：Get("/a/b")。
预期：返回含 id=1 与处理器效果（继承链保留）。
验证：比对 JSON 字段。〔已固化：TestBuildTree〕

## S-002：lazy 树首查触发 realize 与上下文填充

前置：NewLazyTree，模板 `{}`，directive 仅挂深层 `/a/b`，探针 RawProcessor。
动作：GetWithContext(rc with Params, "/a/b")。
预期：1. build 期无计算，首查才执行；2. 闭包收到 rc.Params；
3. 闭包收到 TreePath="/a/b"（a208275 起，含 S-007 回归探针）。
验证：探针断言。〔已固化：TestTree_GetWithContext，三断言全绿〕

## S-003：cache TTL 过期重算 / 零 TTL 永久缓存 / 父刷新子重组

前置：NewLazyCacheTree TTL=50ms 计数处理器；另一棵 TTL=0；再一棵深层
节点 + 父节点内容可刷新。
动作：TTL 树隔 >TTL/<TTL 各查一次；零 TTL 连查；刷新父后再查子。
预期：TTL 内同值、过期新值；零 TTL 永远同值；父发布新版本后子缓存
组合过期、下次查询重组（min(自身TTL,父版本) 规则）。
验证：比对返回值。〔已固化：TestLazyCacheTree_TTLExpiry/ZeroTTL、
TestStressParentRefreshChildRecomposes（#48）〕

## S-004：fallback 触发边界（D-005 契约）

前置：标准树含根 directive，SetFallback 记录调用并追加标记。
动作：分别 Get("/nonexistent") 与 Get("/")。
预期：未命中路径 → fallback 触发；命中根 → 不触发、返回原内容。
验证：fallbackCalled + 输出比对。〔已固化：TestTree_Fallback〕

## S-005：GetWithContext 参数直达 fallback

前置：同 S-004，fallback 为捕获 rc 的处理器。
动作：GetWithContext(rc{Params:{user:bob}}, "/missing")。
预期：fallback 收到同一 rc，Params.user=bob。
验证：探针断言。〔已固化：TestTree_FallbackWithContext〕

## S-006：CURL 失败的结构化诊断与脱敏

前置：CURLProcessor 指向本地 httptest server；一组 500（敏感头
Authorization、URL query 含 api_key），一组连接拒绝。
动作：Process 触发两种失败，检查错误。
预期：1. errors.As 命中 *CURLProcessError，500 场景含 StatusCode/头/体/
Duration；2. Error() 文本敏感值为 [REDACTED]、体截断 4KB；3. 结构化字段
保留原值；4. 2xx 透传响应体。
验证：curl_test.go 断言方式。〔已固化：curl_test.go〕

## S-007：TreePath 传播回归探针（原基线红，已转绿）

前置：同 S-002 探针。
动作：GetWithContext 到 /a/b 后读 rc.TreePath。
预期："/a/b"（a208275 恢复赋值；d016ff0~41595a5 期间曾为空串）。
验证：go test -run TestTree_GetWithContext。〔已固化，绿〕

## S-008：forest 树名缺失的两条路径

前置：NewForest 建树 json_tree_1；未知名 "nope"。
动作：a) f.GetVal("nope","/x")；b) f.Get("nope").Get("/x")；
c) GET /api/v1/rule?name=nope。
预期：a) ErrNotExistsTree；b) panic（nil 接口方法调用，API 未变，
T-002R）；c) 404 JSON（#64 后不再 panic）。
验证：a) errors.Is；b) recover；c) handler 断言。
〔已固化：a/forest 层 forest_test.go、b 无需固化（预期 panic）、
c TestGetRule_MissingTree〕

## S-009：lazy 树限流

前置：NewLazyTree + SetRateLimit(1,1)，幂等处理器；并发 2 次 Get。
预期：第一次成功；限流窗口内的重 realize 拿 ErrRateLimited；标准模式
build 期 realize 不受限流影响。
验证：errors.Is(err, ErrRateLimited)。〔已固化：tree_test.go/forest_test.go
限流用例 + TestStressRateLimiterDeterministic/SwapConsistency〕

## S-010：builder panic 不炸 forest

前置：Register 一个必 panic 的 builder + 一个正常 builder；Build()。
预期：panic 被记录，正常树可用，Build 不中断、失败树缺失。
验证：f.Info() 与日志。〔已固化：TestForest_RegisterPanickingBuilder〕

## S-011：动态层缓存不串参（D-009 契约）

前置：标准树，链 = [JSONProcessor(静态), TemplateProcessor(pattern 含
${user}), JSONProcessor(静态后段)]。
动作：先 GetWithContext(user=alice) 再 GetWithContext(user=bob)，再 Get()。
预期：两次各得对应参数结果；节点缓存（Get 与后续查询看到的静态基座）
不含任何参数渲染痕迹；Get() 返回占位符原样内容；中间节点挂动态层时
深查不执行中间动态层。
验证：输出比对 + 缓存探针。〔已固化：param_test.go 五测试
（CacheNotPolluted/StaticPrefixCached/GetReturnsStaticBase/
InheritanceComposition/IntermediateDynamicNotApplied）〕

## S-012：CURL 安全开关与取消透传（D-010）

前置：httptest TLS 自签 server（body=tls-ok）；普通 server 返回 1KiB；
客户端可取消的 rc.Context；IVY_CURL_ALLOW_HOSTS 设为无关域名。
动作与预期：1. Insecure=true + TLS server → 成功；默认 → 证书错误；
2. MaxBytes=64 → 报错且错误文本含 max_bytes；=2048 → 全量；=-1 → 不限；
3. 白名单不匹配 → 连接前拒绝；302 跳到白名单外主机 → 拒绝；
4. 已取消的 rc → 出站请求即刻中止（CURLProcessError 包 context.Canceled）；
5. Save/Load 往返保留 insecure/max_bytes。
验证：curl_optin_test.go、TestGetRule_CancelsUpstreamFetch。〔已固化〕

## S-013：serve 配置生命周期（D-012）

前置：RULES_FILE 指向 t.TempDir 规则文件，RULES_TTL=50ms；启动后改文件。
动作：SIGHUP；改坏文件再 SIGHUP；TTL 过期后首查。
预期：好文件重载后新规则生效（换 forest 原子）；坏文件重载保留旧
forest 且进程存活；TTL 树过期后首查重放 processors（如 curl 重新拉取）；
无任何后台定时重建（观察无周期性 processor 执行）。
验证：〔已固化：load_fail_loud_test.go、TestRefreshOnDemandOnly、
TestDefaultCacheBuilder_TTLRefresh〕
