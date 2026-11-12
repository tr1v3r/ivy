# ivy 架构文档

> ivy（`github.com/tr1v3r/ivy`）是一个分层内容构建引擎（hierarchical content-construction engine）：
> 内容从一份根模板出发，沿一棵按路径寻址的树逐层向下流动，每个节点用一条 Processor 链
> 对"从父节点继承来的内容"做增量变换，查询一个路径即得到被沿途每一层逐步塑形后的结果。

## 1. 核心概念词典

| 领域词 | 含义 | 代码中的真实例子 |
|---|---|---|
| **Forest** | Tree 的集合管理者：持有 `TreeBuilder` 列表，负责构建、按名存取、定时刷新与全局限流 | `forest.go` 的 `forest` 结构体实现 `export.go` 的 `Forest` 接口；`NewForest(builders...)`（`factory.go`）创建并立即 `Build()` |
| **Tree** | 分层节点结构：每节点持有 content、processor 链与 children，按路径逐层 `Get`/`GetWithContext` | `tree.go` 的 `tree` 结构体；`NewLazyJSONTree("cfg", tmpl, directives...)` 建出一棵 lazy JSON 树 |
| **Directive** | "路径 + Processor 链"的一对：声明在某路径上要套用哪些变换 | `directive.go` 的 `directive{path, processors}`；`ivy.NewDirective("/api/v1/users", procA, procB)` |
| **Driver** | 可插拔的格式层，组合三种能力：`PathParser`（路径语义）+ `Realizer`（执行 Processor 链）+ `Modem`（Processor 的序列化） | `driver/json.go` 的 `JSONDriver`，由 `driver.NewJSONDriver()` 组装 `SlashPathParser` + `StdRealizer` + `GeneralModem[*JSONProcessor]` |
| **Processor** | 最小变换单元：`Process(rc, before) (after, error)`，把内容变换一步 | `driver.JSONProcessor`（sjson 增删改）、`driver.CURLProcessor`（HTTP 拉取）、`driver.TemplateProcessor`（`${param}` 插值） |
| **ParamAware** | 可选能力接口：声明该 Processor 的输出依赖请求参数，从而进入"动态层"（每查询重算、永不入缓存） | `driver.TemplateProcessor` 同时实现 `driver.ParamAware`（`param.go`）；`tree.dynamicSplit()` 据此切分链 |
| **RealizeContext** | 每次查询携带的运行时上下文：`Params`（请求参数）、`ParentContent`（父节点内容）、`TreePath` | `web/handler.go` 的 `GetRule` 把 URL query 填进 `driver.RealizeContext{Params: ...}` 传给 `GetWithContext` |

四种求值模式（同一 `tree` 结构体的开关组合，见 `tree.go` 字段注释）：

- **standard**：初始建树时把静态前缀算完缓存；挂载后缓存失效，首次访问及父内容版本更新时重新组合；
- **lazy**：配置可先建立节点拓扑，静态内容延后到首次访问时计算；
- **instant**：每次访问强制重算；
- **cache TTL**：lazy 语义 + `realizedAt`/`cacheTTL` 过期重算。

挂载保留每个节点的求值模式，但 `Graft` 本身不执行任何处理器。

## 2. 模块地图

| 目录/文件 | 职责 | 关键文件 |
|---|---|---|
| 根目录 `package ivy` | 引擎核心：Tree/Forest/Directive 与全部构造工厂 | `export.go`（公共接口）、`tree.go`、`forest.go`、`directive.go`、`factory.go`、`errors.go` |
| `driver/` | 可插拔格式层：各格式 Driver + Processor + 通用组件 | `export.go`（Driver/Processor/ParamAware/RealizeContext 定义）、`common.go`、`modem.go`、`json.go`、`yaml.go`、`toml.go`、`xml.go`、`tile.go`、`curl.go`、`param.go`、`raw.go`、`dummy.go`、`errors.go` |
| `web/` | HTTP 暴露层：gin 路由、规则查询 handler、进程级 Forest 生命周期 | `server.go`（`Serve`/`InitForest`/`RefreshForest`/`DefaultBuilder`/`webDriver`）、`route.go`、`handler.go`（`Ping`/`GetRule`） |
| `cmd/serve/` | 服务器入口：读规则文件 → 组装 Directive → 起 HTTP | `serve.go`（`main`/`load`/`register`） |
| `conf/` | 默认规则文件（JSON 编码的 Directive 列表） | `conf.json`、`rules.json` |
| `docs/` | swag 生成的 Swagger 产物（`/swagger/index.html`） | `docs.go`、`swagger.json/yaml` |

## 3. 核心抽象的接口设计

公共 API 全部收在两个 export 文件中（`export.go` 与 `driver/export.go`），实现类型均为小写未导出（`forest`、`tree`、`directive`），接口即边界。

### 3.1 引擎侧（`export.go`）

```go
type Forest interface {
    Register(...TreeBuilder); Append(...TreeBuilder) Forest
    Build() Forest
    Refresh(interval ...time.Duration)   // 带间隔时阻塞循环重建
    RefreshTree(name string)
    Get(name string) Tree; Set(tree Tree)
    GetVal(treeName, path string) ([]byte, error)
    GetValWithContext(rc *driver.RealizeContext, treeName, path string) ([]byte, error)
    Info() string
    SetRateLimit(r rate.Limit, burst int)
    SetDefaultContext(rc *driver.RealizeContext)
}
```

- 实现方：`forest`（`forest.go`），唯一实现。
- 为什么这样切：Forest 只做"集合 + 生命周期"，不理解路径与内容；限流（`SetRateLimit`）是全局兜底，树级限流由 `Tree.SetRateLimit` 单独提供——两层防护互不耦合。

```go
type Tree interface {
    Name() string; Path() string
    Set(Directive) error
    Get(path string) ([]byte, error)
    GetWithContext(rc *driver.RealizeContext, path string) ([]byte, error)
    Has(path string) bool; Del(path string) error
    Graft(path string, source Tree) error
    ShowStruct() []byte
    SetRateLimit(r rate.Limit, burst int)
    SetFallback(proc driver.Processor)
    SetDefaultContext(rc *driver.RealizeContext)
}
```

- 实现方：`tree`（`tree.go`），递归组合（children 也是 `Tree`）。
- 切分理由：`Set` 建树、`Get/GetWithContext` 读路径（后者支持参数化动态层），`Graft` 按目标路径直接挂接同一个子树对象，`SetFallback` 兜住"路径解析不到子节点"的场景（如通配路由）。
- 路径契约：所有公开路径调用相对接收者自己的树根解析，不以挂载后的绝对 `Path()` 为输入前缀；推荐始终从最外层 root 操作。内置实现的 `Path()` 动态沿父节点和挂载别名计算，`ShowStruct()` 的键使用挂载别名，不使用源树 `Name()`。
- **Breaking change**：旧 `Graft(Tree)` 改为 `Graft(path string, source Tree) error`，调用者和自定义 `Tree` 实现均需适配；不提供 `Clone` 或独立的 `GraftAt` 函数。
- 接口不规定锁策略，也不承诺任意自定义树都具备内置实现的继承、位置维护和并发语义。

```go
type Directive interface {
    Path() string
    Processors() []driver.Processor
}
```

- 实现方：`directive`（`directive.go`）；同文件还提供泛型排序设施 `directives[R]`/`by[R]`/`byLevel()`，建树时按路径层级从浅到深应用规则。

### 3.2 格式层侧（`driver/export.go`）

```go
type Driver interface { Name() string; PathParser; Realizer; Modem }

type PathParser interface {
    GetLevel(path string) int                       // 路径有几层
    GetNameByLevel(path string, level int) string   // 取第 level 段段名（1-based）
    AppendPath(path, name string) string
}

type Realizer interface {
    Realize(rc *RealizeContext, rule []byte, ops ...Processor) ([]byte, error)
}

type Modem interface {
    Marshal(...Processor) ([]byte, error)
    Unmarshal(data []byte) ([]Processor, error)
}

type Processor interface {
    Path() string; Type() string
    Process(rc *RealizeContext, before []byte) (after []byte, err error)
    Author() string; CreatedAt() time.Time
    Load([]byte) error; Save() []byte
}
```

- 实现方与组装方式：`JSONDriver`/`YAMLDriver`/`TOMLDriver`/`XMLDriver`/`TileDriver`（`driver/*.go`）以及 `web/server.go` 的 `webDriver`、测试用 `DummyDriver`。它们都是**结构体嵌入三个小接口**的组装产物，而不是写一个大类——三种关注点（路径语义 / 执行 / 序列化）可独立替换。
- 通用件：`DelimiterPathParser`（`common.go`，预置 `SlashPathParser`）、`StdRealizer`（顺序套用 Processor，错误包装带 `Type()`/`Path()`）、`GeneralModem[T]`（`modem.go`，反射构造 `T` 再逐个 `Load`；`DummyModem` 为不可序列化场景的 no-op）。
- 为什么这样切：格式差异被压缩到"Processor 怎么改文档 + Modem 怎么序列化 Processor"，树的遍历/缓存/继承逻辑完全格式无关；新增格式只需实现一个 Processor + 组装一个 Driver。

### 3.3 动态层契约（`ParamAware`）

```go
type ParamAware interface { ParamKeys() []string }
```

`tree.apply` 调 `dynamicSplit(procs)` 找到**第一个**实现 `ParamAware` 的 Processor 下标存入 `dynamicFrom`：

- 静态前缀 `procs[:dynamicFrom]`：按 standard/lazy/instant/TTL 原语义 realize 并缓存；
- 动态层 `procs[dynamicFrom:]`：仅在**目标节点**由 `dynamicRealize` 在每次 `GetWithContext` 时叠加请求上下文重算，结果只返回给调用者、**从不写回节点缓存**（`tree.go` 注释明确此契约）——不同参数永不互相污染。

### 3.4 直接挂接与局部路径递归（`Tree.Graft`）

`root.Graft("/api/config", source)` 直接把 **source 本身** 挂到指定位置，
不复制对象。目标父节点 `/api` 必须存在，目标不能是接收者的根；原目标子树整体替换。
`source.Name()` 可以与别名 `config` 不同，原生 `source.Path()` 根据新父链返回
`/api/config`。继续执行 `root.Graft("/api/config/live", other)` 会进入已经挂载的树。

入口只按源类型分派一次：`graftTree` 接收非空 `*tree`，集中处理原生源的锁、归属和
缓存；`graftOther` 接收其他 `Tree`，只修改目标连接，不操作源的内部状态。两条流程
复用 `locateGraftTarget` 查找目标。遇到自定义目标时，统一在 helper 释放锁之后由入口
调用 `targetTree.Graft(remainingPath, source)`，不把可空的原生源参数贯穿各个步骤。

```text
【局部寻址：入口解析一次，cursor 逐段消费】
root.Graft("/api/config/live", source)
│ root   剩余 api/config/live → 找到 api
│ api    剩余 config/live     → 找到 config（可能是此前挂载的树）
└─ config 剩余 live           → 替换 children["live"]
   └─ 遇到自定义 Tree → 公开方法只接收剩余后缀字符串，不传原始整树路径

【内置实现的直接挂载】
校验：源必须未挂载；拒绝自我挂载、环与重复归属
│ 保留同一源对象及其 Driver / Processor / 模式 / 限流器 / 上下文对象
│ 记录 parent + mount alias，不递归写入绝对 path / level
│ 失效源子树的模板基底和处理结果缓存，以便继承新父内容
└─ 替换目标引用；被替换的原生子树解除归属，Del 同样解除归属
   └─ 脱离父节点后的子树可重新挂载到其他位置

【求值：Graft 不执行处理器】
挂载后的首次 Get → 从新父内容计算，包括 standard 节点
│ 父版本未变 → 按节点自身模式命中 / TTL 过期 / instant 重算
└─ 父版本更新 → 所有模式重新组合，包括 standard 和更长 TTL 的子节点
   └─ nil 父内容也有效；从父基底开始，不从上次处理结果重复叠加
```

所有公开路径调用均使用接收者的局部根坐标。若 `branch` 挂载于 `/api/config`，
`branch.Has("/users")` 与 `root.Has("/api/config/users")` 定位同一个节点，
但 `branch.Has("/api/config/users")` 不会自动剥去挂载前缀。原生查询上下文的
`TreePath` 仍可使用动态推导的挂载位置；寻址本身不依赖节点的旧绝对层级。

挂载后保留的 `source` 变量仍指向同一个对象，局部编辑会反映到目标树；推荐后续操作
统一走最外层 root，避免混淆局部路径与位置展示。挂载不会复制或重置限流器、替换源的
Driver 或默认上下文对象。格式兼容或转换由调用者负责。参数动态层仍只作用于查询目标，
不会进入静态缓存。通过已挂载句柄直接查询时，先按父链求值祖先静态内容，确保首访
和祖先刷新后也获得正确继承；祖先动态层仍跳过。原生节点名称必须能在所有外层原生
路径语法中表示，挂载及后续插入时都会校验，避免产生无法通过 root 寻址的节点。
原生查询还携带挂载身份，并在求值锁内校验；并发换父节点时从查询入口重试，防止
旧父节点的较大版本号污染新位置的缓存。名称校验与新节点插入处于同一拓扑临界区。
拓扑锁归属于每个原生实例：结构操作解析并锁定当前外层 root，跨树移植只按固定顺序
锁源与目标涉及的 root；拿锁后重新校验当前归属，迁入未锁定 root 时释放并重试。
独立树之间没有包级锁竞争。子树脱离时启用自身实例锁，不递归改写后代的锁引用。

以上继承、动态位置和单父归属保证属于内置实现；不能据此推断任意自定义 `Tree`
也能做到相同处理。接口仅规定方法和路径委派边界，并发与锁策略由具体实现负责。
`Forest.Refresh` / `RefreshTree` 重跑 `TreeBuilder` 后替换树，不能自动保留旧树上的
运行时挂载；需在 builder 中重新构建源树并执行挂载。

## 4. 构建流程走透

### 路径 A：HTTP 服务（JSON 规则文件 → webDriver 树 → 查询）

1. **入口** `cmd/serve/serve.go:main`：读环境变量 `SHUTDOWN_TIMEOUT`，调 `load()`。
2. **`load()`**：读 `RULES_FILE`（默认 `../../conf/rules.json`），反序列化为 `[]RuleDataItem{Path, Processors[]{Type, Data}}`；按 `type` 字符串分发到 `driver.JSONProcessor`/`YAMLProcessor`/`CURLProcessor`/`XMLProcessor`/`TOMLProcessor`/`TemplateProcessor`，逐个 `op.Load(opData.Data)`，最后 `ivy.NewDirective(line.Path, ops...)` 组装出 `[]ivy.Directive`。
3. **`web.InitForest(web.DefaultBuilder(directives...))`**（`web/server.go`）：`DefaultBuilder` 返回一个 `TreeBuilder`，内部用 `webDriver`（`SlashPathParser` + `StdRealizer` + `DummyModem`）调 `ivy.NewTree(driver, "default", "{}", directives...)` 建 standard 模式树；`InitForest` 再 `ivy.NewForest(builders...)` 触发 `forest.Build()`。
4. **建树** `factory.go:NewTree` → `newTree` → `buildTree` → `tree.build(directives...)`：`byLevel()` 按路径深度排序后逐条 `tree.Set(r)`；遍历按局部路径游标下钻，目标节点记录 `procs` 并计算 `dynamicFrom`，standard 模式初始构建时计算静态前缀。内部创建节点与公共 `Graft` 的移植操作分离，不再依赖绝对 `t.level` 判断目标。
5. **配置与内容刷新**：SIGHUP 重载规则配置，`RULES_TTL` 控制内容缓存过期后的按需求值；不靠周期性重建整片 Forest 刷新内容。显式调用 Forest 刷新仍会重跑 builder，组合树必须在 builder 中重新挂载。
6. **查询** `GET /api/v1/rule?name=...&path=...`（`web/route.go` → `web/handler.go:GetRule`）：把除 `name`/`path` 外的所有 query 参数塞进 `driver.RealizeContext.Params`，调 `f.Get(name).GetWithContext(&rc, path)`。
7. **树内遍历**（`tree.go:GetWithContext`）：入口解析一次局部路径，游标逐段消费。每个原生节点先校验/计算静态缓存，再携带成对的父内容与版本继续下钻；目标节点执行参数动态层，缺失节点走 fallback。`TreePath` 从挂载父链推导，定位不依赖旧绝对层级。跨自定义 `Tree` 边界时仅传剩余后缀字符串，对方自行定义继承和并发行为。
8. **落盘响应**：`GetRule` 把结果 JSON 写回客户端；`Serve`（`web/server.go`）负责 `:8080` 监听与信号驱动的优雅关停。

### 路径 B：SDK 内嵌（curl 拉远程内容 → YAML 树，`export_test.go` 有等价用例）

```go
tree, _ := ivy.NewYAMLTree("local", "", ivy.NewDirective("/", &driver.CURLProcessor{URL: url}))
rule, _ := tree.Get("")
```

1. `factory.go:NewYAMLTree` → `NewTree(driver.NewYAMLDriver(), ...)`；
2. 建树时 `apply` → `realize` → `StdRealizer.Realize` 顺序执行 `CURLProcessor.Process`：HTTP 拉取，非 2xx 返回带完整诊断与敏感字段脱敏的 `*CURLProcessError`（`curl.go`）；
3. `Get("")` 命中根节点，返回已缓存内容。换 `NewLazyCacheJSONTree(name, tmpl, ttl, ...)` 即得"懒加载 + TTL 过期重算"变体（`engine_test.go` 的 `TestLazyCacheTree_TTLExpiry` 验证该行为）。

## 5. 内部依赖关系

```mermaid
graph TD
    subgraph cmd
        SERVE[cmd/serve/serve.go<br/>main / load / register]
    end
    subgraph web
        SERVER[web/server.go<br/>Serve / InitForest / DefaultBuilder / webDriver]
        ROUTE[web/route.go<br/>RegisterAPI]
        HANDLER[web/handler.go<br/>Ping / GetRule]
    end
    subgraph ivy 根包
        EXP[export.go<br/>Forest / Tree / Directive 接口]
        FOREST[forest.go<br/>forest 实现]
        TREE[tree.go<br/>tree 实现 + 四种模式]
        DIR[directive.go<br/>directive + 排序]
        FACT[factory.go<br/>New* 构造工厂]
        ERR[errors.go<br/>ErrNotExistsTree / ErrRateLimited]
    end
    subgraph driver 包
        DEXP[driver/export.go<br/>Driver / Processor / ParamAware / RealizeContext]
        COMMON[driver/common.go<br/>DelimiterPathParser / StdRealizer]
        MODEM[driver/modem.go<br/>GeneralModem]
        DRIVERS[driver/json.go yaml.go toml.go xml.go tile.go<br/>格式 Driver + Processor]
        PROCS[driver/curl.go param.go raw.go dummy.go<br/>跨格式 Processor]
    end
    EXT[外部: tr1v3r/pkg (log/fetch/guard),<br/>tidwall/sjson, pelletier/go-toml v2,<br/>gopkg.in/yaml.v3, golang.org/x/time/rate, gin]

    SERVE -->|load() 组装 Directive| EXP
    SERVE -->|NewDirective| FACT
    SERVE -->|processor 分发| DEXP
    SERVE --> SERVER
    SERVE --> ROUTE
    ROUTE --> HANDLER
    HANDLER -->|f.Get().GetWithContext| EXP
    SERVER -->|InitForest/DefaultBuilder/webDriver| EXP
    SERVER -->|webDriver 组装| COMMON
    FOREST -.实现.-> EXP
    TREE -.实现.-> EXP
    DIR -.实现.-> EXP
    FACT --> TREE
    FACT --> DRIVERS
    TREE -->|realize / Realize| DEXP
    TREE -->|ParamAware 类型断言| DEXP
    FOREST --> TREE
    DRIVERS --> COMMON
    DRIVERS --> MODEM
    PROCS --> DEXP
    DEXP -.被引用.-> TREE
    DRIVERS --> EXT
    PROCS --> EXT
    TREE -->|rate.Limiter| EXT
    FOREST -->|rate.Limiter| EXT
```

要点：`ivy` 根包与 `driver` 包单向依赖（ivy → driver），`driver` 不回引 ivy；web/cmd 只依赖公共接口。`engine_test.go` 里没有 `engine.go`——"engine"指的是根包这套 Forest/Tree/Directive 协作体，测试直接以包内白盒方式驱动它。

## 6. 测试策略（从 `*_test.go` 与 `export_test.go` 推断）

- **双层测试**：根包 `engine_test.go`/`forest_test.go`/`tree_test.go` 是 `package ivy` 白盒（能构造 `forest`、观察 builder panic 被包装、TTL 过期时序）；`export_test.go` 是 `package ivy_test` 黑盒，只用导出 API——同一仓库同时守住"实现细节正确"与"公共契约可用"两条线。
- **行为契约驱动的命名**：`TestParamAware_CacheNotPolluted`、`TestParamAware_StaticPrefixCached`、`TestParamAware_GetReturnsStaticBase`、`TestParamAware_IntermediateDynamicNotApplied`（`param_test.go`）逐条验证第 3.3 节的动态层契约；`TestLazyCacheTree_TTLExpiry`/`ZeroTTL` 验证缓存语义；`TestForest_RegisterPanickingBuilder` 验证 builder panic 被 `appendBuilders` 的 recover 包装吃掉。
- **不依赖外网**：`export_test.go` 的 `newTestServer(t, body)` 用 `httptest.Server` 本地起 HTTP，curl 类测试零外部网络（注释明言此意图）。
- **driver 层表驱动 + 元数据全覆盖**：`driver/metadata_test.go` 逐一检查每个 Processor 的 `Author/CreatedAt/Load/Save` 往返与 Driver 名；`yaml_test.go`/`toml_test.go`/`xml_test.go` 按操作类型（create/set/replace/delete、空输入、未知类型）逐项断言。
- **错误路径是一等公民**：`curl_test.go` 专测失败诊断（HTTP 非 2xx、请求构造失败、URL 脱敏）、`TestStdRealizerWrapsErrors`、web 的 `TestGetRule_ErrorBranch`、serve 的 `TestLoad_DefaultFileMissing`/`InvalidJSON`。
- **进程级集成测试**：`cmd/serve/serve_test.go` 直接调包内 `load`/`register` 验证规则装载与路由注册。

## 7. 设计决策与取舍

- **接口在 export.go、实现全小写**（推断自文件组织）：强制消费方面向 `Forest/Tree/Directive` 编程，未来可换实现。
- **Driver = PathParser + Realizer + Modem 三接口嵌入**（代码结构自明）：路径语义、链执行、序列化三者正交，`DelimiterPathParser.WithDelimiter` 让非斜杠格式（如 XML 内部路径）可复用全部树逻辑。
- **四种模式用布尔/字段开关而非子类**：lazy/instant/TTL 通过节点自身的缓存判断分支表达，避免类层次膨胀；混合模式挂载保留这些字段，不改变源节点类型。
- **动态层"只返回不写缓存"**（`tree.go` 注释明示）：让参数化查询与四种缓存模式正交共存——这是本引擎最核心的取舍：宁可每次查询重放动态层（性能），也要保证静态缓存与参数无关（正确性）。
- **内置实现的缓存同步**：读锁快路径 + 写锁内复查，避免并发下重复 realize；这属于具体实现机制，不是 `Tree` 接口向其他实现施加的锁策略。
- **限流按节点自身模式执行**：原生 standard 节点初始建树时求值，挂载后仍可能因首次访问或父版本更新而重新求值；挂载不把它转换成其他模式，也不复制源限流器。
- **builder panic 被 recover 包装**（`forest.appendBuilders`）：单个树构建失败不拖垮整个 forest 刷新（测试显式覆盖）。
- **`CURLProcessError` 的诊断/脱敏双轨**（`curl.go` 注释）：`Error()` 里敏感头与 URL 密码打码、正文截断到 4KB，结构化字段保留原值给显式检查错误的调用方——可观测性与安全兼顾。
- **序列化能力是 Processor 的可选属性**：`RawProcessor`/`CombinedProcessor` `Load` 恒败（`ErrSerializeNotSupport`），`GeneralModem` 拒绝接口类型参数 T——代码内变换与配置文件规则两个世界显式分离。
- **web 管理面是占位**（`route.go` 注释）：只有 `GET /api/v1/rule` 完整实现，`template/info/mod/list/config` 均为 `Ping` 占位，引擎核心与 HTTP 面解耦推进。
- **一处已实锤的配置键漂移（2026-08-31 诊断并修复）**：`conf/rules.json` 的键名曾是 `"operators"`，而 `serve.go:RuleDataItem` 的标签是 `json:"Processors"`——2023-10-18 提交 `8e60da4`（rename Operator to Processor）改了代码标签但漏改该默认文件，`encoding/json` 对不匹配键静默忽略，导致默认配置的 curl processor 在启动时被丢弃、`/` 路径挂空 directive（现有 `TestLoad` 只用自建 fixture，从未覆盖默认文件，因此逃过 CI）。已将 conf 键名修正为 `"Processors"`，并新增 `cmd/serve/rules_config_contract_test.go` 钉死默认文件必须解析出非空 processor 链，防止同类静默漂移复发。

## 8. 验证边界

架构契约的验证应覆盖：公开路径的接收者局部语义、跨自定义树的后缀委派、直接对象引用、
挂载别名与动态位置、解除归属后重新挂载、混合模式下父版本传播，以及 Forest builder 重建。
并发测试针对具体树实现，不把内置锁机制当作所有 `Tree` 实现的接口保证；实际执行结果以
当前变更的测试与 CI 记录为准。
