# ivy 修改历史（整理版）

证据分级：A = 提交信息/代码可回查；C = 推测。2026-10-12 整理复核，HEAD = 1689bbf。
（41595a5 之前的 H-001~H-008 本轮抽查仍与代码一致，仅压缩排版。）

## H-001：2023-10「格式无关引擎」定型三连改

变化：Operator→Processor（8e60da4）、string→[]byte（434ab5b）、
calculator→realizer（5932981）、泛型 GeneralModem（d2f744e）。
原因（A）：统一术语/数据面、去重复代码。关联 D-001、D-008。

## H-002：lazy / instant / cache 三种模式渐进引入

变化：lazy（2023-10 #9）→ instant（2024-10 b30b1f1）→ cache TTL
（2026-04 04d1739）；47902a4 改用 realizedAt，12cbff0 加双检查快路径。
原因（A）：省内存/实时性/TTL 刷新/降锁竞争。模式语义矩阵见 FLOW.md。

## H-003：builder panic 防护（bbdb0ee，2024-01）

变化：appendBuilders 用 recover 包装 builder，panic 记日志跳过该树。
副作用（A）：构建失败静默降级为「树缺失」。现有
TestForest_RegisterPanickingBuilder 固化此行为（S-010）。

## H-004：2026-04-14 上下文大重构（一天 7 提交）

变化：RuleContext 全签名穿透（064ec05）→ fallback 处理器 +
CombinedProcessor（5e679c8）→ RawProcessor 闭包加 rc（8bc8f84）→
Ctx() 访问器（a0e9e40）当天回退（79487ad）→ defaultCtx 双阶段兜底（9511114）。
回退原因未记录（C）。当前影响：勿再补访问器（D-003）。

## H-005：#15 移除 TreePath 赋值与 CURL Params 注入（d016ff0，2026-04-15）

变化：GetWithContext 不再设 rc.TreePath；CURL 不再用 rc.Params 拼 query。
当时实测（A，41595a5）：全库无 TreePath 赋值点，engine_test 断言失败
（T-001 由而来）。
后续（A）：a208275（#17，2026-08-23）恢复下钻赋值 rc.TreePath/
rc.ParentContent，测试转绿；Params 能力由 #29 动态层接棒（D-009，
D-004 同时失效）。本条已闭环。

## H-006：更名 rule→ivy 与公开 API 去 rule 化（2026-04-17）

变化：e71dc28 更名、4390f6e RuleContext→RealizeContext（17 文件）。
当前影响：import path 为 github.com/tr1v3r/ivy；查历史注意符号换名。

## H-007：leaf fallback 语义修复（d3a14d4，2026-04-18）

变化：命中目标节点直接返回，不再落 doFallback；断言翻转。
当前影响：D-005 行为契约；改回旧行为破坏 TestTree_Fallback。

## H-008：CURL 错误诊断体系（#16，41595a5，2026-07-18）

变化：CURLProcessError 结构化 + Error() 脱敏/截断 + Unwrap。
具体故障背景未记录（C）。后续 #87/#83 扩为完整安全姿态（D-010、H-013）。

## H-009：测试基线演变

2026-10-12（41595a5）实测三个红：TestTree_GetWithContext（TreePath）、
TestForest_run panic（树名不匹配）、TestYAMLProcessor（依赖仓外绝对路径
fixture rule.yml）。
2026-10-12（1689bbf）复核：`go test -count=1 ./...` 全绿——三红均由
a208275（#17 CI 落地）修复：恢复 TreePath 赋值、修测试树名、fixture 内联。
当前影响：全绿基线 + CI（race/lint/govulncheck）把关；此后「测试通过」
是有效信号，不再有已知噪音可归咎。

## H-010：工程化落地周（2026-08，#17/#27/#30 + docs）

变化：CI 管线（lint、-race 全量、govulncheck）+ lint 规则集 + revive
导出注释强制（5857626）；README 扩写（e0a6916）；ARCHITECTURE.md（92a4629）；
Makefile；依赖批量升级（#18-#26）；conf/rules.json 键名 operators→Processors
修复（49df41e，8e60da4 改代码漏改配置导致的静默丢弃，见 ARCHITECTURE §7）。
原因（A）：让上述长期红的测试无法再被无视。

## H-011：参数化动态层（#29/#67/#66，2026-08-24）

变化：ParamAware 接口 + TemplateProcessor（driver/param.go）+ 链切分
（dynamicSplit/dynamicRealize）；CombinedProcessor 内含参数感知处理器时
整体视为参数感知（#67）；Set 换处理器链即失效缓存（#66）。
原因（A）：#15 移除 CURL 参数注入后，动态构造能力缺口需要缓存安全的
引擎级答案。关联 D-009（取代 D-004）。

## H-012：tree 并发与缓存正确性周（2026-09-14~15，#63/#71/#48/#79/#78）

变化：realize 恒从 base 起算止叠乘（544f11f）；lazy 继承决策入临界区
（a78ad9c）；父发布版本传播 + min(自身TTL,父版本) 新鲜度（0f69191）；
Set 族写同步（1df0a02）；路径每次下钻解析一次（b1cda2c）。
原因（A）：各 commit/issue 记录 -race 与压测暴露的竞态/叠乘缺陷；
配套 engine 层 stress 套件（a5e0c8b）。关联 D-011。

## H-013：driver 容错与 CURL 安全加固（#62/#65/#69/#86/#84/#81/#28/#87/#83）

变化：JSON 空 value set 报错（b731159）、YAML 多文档拒绝（899f6c7）、
XML 命名空间前缀保留（b250cae）、JSON 非合法基底拒绝（0d4685c）、
GeneralModem 非法类型参数显式报错（e4bae3c）、GetNameByLevel 越界不
panic（d63b65f）、YAMLProcessor 操作补全（a4500c9）；CURL 安全姿态
（964a17c，D-010）与取消透传（497ce25）。
原因（A）：各 issue 记录——多为「静默产出非法内容/内存耗尽/MITM」类缺陷。

## H-014：web/serve 运行面加固（#64/#68/#72/#70/#75/#58/#60/#82）

变化：未知树名 404 不 panic（5801473）；全局 forest 挂锁（5037ebe）；
DefaultBuilder 记日志不 panic（8407617）；去 5s 全量重建（604ba35）；
SIGHUP 重载 + RULES_TTL（0aa81cb，D-012）；load fail-loud（da1758c）；
未知 processor 类型丢弃并报错（3aceb1f）；默认规则路径 repo-root 优先
探测（1689bbf，修 `go run ./cmd/serve` 找不到 conf/rules.json 静默空树）；
web/serve 层压测套件（aca1a0c）。
注意：AGENTS.md/ARCHITECTURE.md §4 的「默认 ../../conf/rules.json」「5s
定时刷新」描述滞后于 #82/#70（代码为准，见 TODO T-006）。
