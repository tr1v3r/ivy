# ivy 待办事项

⚠ 待办是背景，不是本次任务的授权清单：看到待办不得顺手执行。
日期 2026-10-12，HEAD = 1689bbf。上轮 T-001~T-005 已全部闭环（H-009），
本文件只保留仍未决事项。

## T-002R：Forest.Get 返回 nil 接口的 API 脆弱性（原 T-002 残留）

状态：等待决策（web 层已修，API 层未动）
问题：`f.Get(未知名)` 返回 nil 接口，`.Get(path)` 即 nil 解引用。
2026-08 #64 后 web/handler.go GetRule 判空返回 404，不再依赖 gin
recovery；但库内 API 形状未变，库使用方仍易踩（S-008b 仍成立）。
影响：SDK 使用方的常见链式写法有 panic 风险；GetVal/GetValWithContext
才是安全路径。
下一步：维护者决策——Get 返回 (Tree, error)（breaking）或文档强制走
GetVal；不决策则保持现状。
完成条件：API 语义明确并落 DECISIONS，或官方文档标注。

## T-004R：README「query hook」路线图条目（原 T-004 重新表述）

状态：候选未验证
问题：旧 README 唯一路线图条目 `query hook` 无展开（e0a6916 重写后该词
已消失）；但 D-005 指出「命中路径统一后处理」语义仍无引擎级答案，
需求可能仍在只是没了文字载体。
影响：方向不明；(C) 级推测与 D-005 的重新评估条件相关。
下一步：维护者确认是否仍规划查询期 hook；若是，落地时同步 FLOW/D-005。
完成条件：明确「做/不做」并记录。

## T-006：AGENTS.md / ARCHITECTURE.md 滞后于代码

状态：已确认待处理（A 级实测：文档与代码不一致，2026-10-12）
问题：① 两处环境变量表仍写 RULES_FILE「default: ../../conf/rules.json」
——#82 后实际为 repo-root `conf/rules.json` 优先、legacy 路径次之的探测，
显式设置原样生效；② ARCHITECTURE.md §4 步骤 5 仍写「每 5s
RefreshForest 全量重建」——#70 已移除，改为 SIGHUP+RULES_TTL；③ §4
步骤 7 的 inherit/realizeWithContext 描述是 #71/#78 前的旧机制。
影响：agent 与新人按文档理解会得出错误行为预期（尤其默认路径与刷新
语义）；违反「文档是 A 级行为证据」的前提。
下一步：维护者更新两文档对应段落（或注明以 knowledge/ 为准的分工）。
完成条件：三处描述与 1689bbf 代码一致。

## T-007：web 管理面路由仍为 Ping 占位

状态：候选未验证（AGENTS.md 自述）
问题：`template/info/mod/list/config` 路由均为 Ping 占位，仅
`GET /api/v1/rule` 完整实现。
影响：管理面缺失；swagger 展示与实际能力不符。
下一步：维护者决定实现优先级或先从路由摘除占位。
完成条件：路由能力与文档一致。

## T-008：D-004 遗留——CURL URL 级模板化缺口

状态：候选未验证
问题：动态层（D-009）覆盖内容级 ${param} 插值，但 CURLProcessor 的
URL/Header/Body 仍是静态配置，无法按请求参数变化（如按参查不同上游）。
影响：需要动态上游的规则仍须自写 RawProcessor。
下一步：若确认需要，把 ParamAware 机制扩展到 CURL 的 URL 模板化；
实现前重估缓存语义（URL 变化=上游变化，动态层重放天然适配）。
完成条件：能力落地并补 SCENARIOS 案例，或明确不做。
