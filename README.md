# gocrud/kernel

`kernel` 是一个 Go 依赖注入与配置框架：`AppBuilder.Build()` 会一次性完成依赖图分析、循环依赖检测，并按依赖顺序把所有服务**提前构造好**；之后的 `Get` 系列只是读取已经构建好的值，不再有任何反射调用、锁或递归，因此解析接近 O(1)。配置能力融合在同一个包里：一步式 `b.Config[T]` 完成"默认值 + 多源合并 + struct 绑定 + 注册"，并可选热重载（fsnotify 文件监控；第三方源可实现 `WatchSource` 走推送）；内置配置选项（`WithFile`/`WithEnv`/`WithReloadable` 等）位于 `kernel/config` 子包。

要求 Go 1.27+（依赖方法级泛型参数，如 `b.Provide[Logger](...)`）。

核心类型：

| 类型/函数 | 说明 |
| --- | --- |
| `New()` / `*AppBuilder` | 注册服务与配置的入口，链式调用 `Provide`/`Config`/`Decorate` 等 |
| `*App` / `b.Build()` | 只读应用，`Build()` 返回时全部单例已构造完成 |
| `Provider[T]` | 可注入的工厂委托：`Get()/MustGet()` 取非 keyed 单例 |
| `Keyed[T]` | 可注入的 keyed 访问器：`Get/MustGet/All(key)` 取具名单例 |
| `ProvideKeyed[T]` + `GetKeyed` | 具名服务：同一类型按 key 区分多个实现 |
| `*Config[T]` | 静态配置包装（`b.Config[T]` / `Configure[T]` / `LoadConfig[T]` 产出） |
| `*ConfigMonitor[T]` | 动态配置（`WithReloadable` / `ConfigureMonitor[T]` 产出） |
| `kernel/config` 包 | 内置配置选项：`config.WithDefaults` / `config.WithFile` / `config.WithMap` / `config.WithEnv` / `config.WithFlag` / `config.WithSource` / `config.WithReloadable` / `config.WithOnReloadError` |
| `Starter` / `Stopper` / `io.Closer`（需 `WithClose`） | 生命周期钩子，`App.Start`/`Stop` 统一驱动 |
| `Extension` + `b.Extend(...)` | 第三方/业务代码编写可复用注册扩展的约定 |

---

### 目录

1. [快速开始](#1-快速开始)
2. [注册服务：Provide](#2-注册服务provide)
3. [两种 factory 形态](#3-两种-factory-形态)
4. [解析服务](#4-解析服务)
5. [生命周期：Start / Stop / Close](#5-生命周期start--stop--close)
6. [TryProvide 系列](#6-tryprovide-系列)
7. [Options 模式：Configure](#7-options-模式configure)
8. [配置：Config[T]](#8-配置configt)
9. [热重载：WithReloadable](#9-热重载withreloadable)
10. [Decorate 装饰器](#10-decorate-装饰器)
11. [扩展机制：Extend](#11-扩展机制extend)
12. [动态解析：Provider[T] 与 Keyed[T]](#12-动态解析providert-与-keyedt)
13. [Keyed Services 具名服务](#13-keyed-services-具名服务)
14. [Build：依赖图、循环依赖检测与诊断](#14-build依赖图循环依赖检测与诊断)
15. [错误处理](#15-错误处理)
16. [完整示例](#16-完整示例)
17. [已知限制](#17-已知限制)

---

### 1. 快速开始

```go
import "github.com/gocrud/kernel"

type Logger interface{ Log(string) }
type consoleLogger struct{}

func (consoleLogger) Log(msg string) { fmt.Println(msg) }
func NewConsoleLogger() Logger        { return consoleLogger{} }

type Repository struct{ Logger Logger }

func NewRepository(logger Logger) *Repository { return &Repository{Logger: logger} }

func main() {
    app, err := kernel.New().
        Provide[Logger](NewConsoleLogger).
        Provide[*Repository](NewRepository). // 依赖 Logger，构造顺序自动排在它之后
        Build()                              // 所有单例在此静态分析、检测循环依赖并提前构造
    if err != nil {
        panic(err)
    }
    defer app.Stop(context.Background())

    repo := app.MustGet[*Repository]()
    repo.Logger.Log("server started")
}
```

三步走：`kernel.New()` 注册 → `Build()` 一次性构造 → `Get`/`MustGet` 只读取。

---

### 2. 注册服务：Provide

```go
b := kernel.New()

b.Provide[Logger](NewConsoleLogger)     // 整个 App 生命周期内只创建一次
b.Provide[Repository](NewSqlRepository) // 依赖 Logger，构造顺序自动排在它之后
```

- `Provide[T]` 是 `*AppBuilder` 上的泛型方法，`T` 可以是接口或具体类型。
- 返回 `*AppBuilder`，支持链式调用。
- **注意**：同一个 `T` 可以多次注册（多实现场景，见 [GetAll](#4-解析服务)）；`Get`/`MustGet` 只返回**最后一次注册**的实现。
- **只提供 Singleton**：没有 Scoped/Transient。所有依赖都在 `Build()` 时静态展开、一次性构造完成；"每次请求一份"这类需求由业务代码在 Singleton 之上自行创建（注入 `Provider[T]` 工厂委托即可）。

---

### 3. 两种 factory 形态

`Provide[T](factory any)` 的 `factory` 参数在注册时通过反射自动侦测形态：

| 形态 | 写法 | 说明 |
| --- | --- | --- |
| 预构建实例 | `b.Provide[Logger](myLoggerInstance)` | 非函数值，直接作为实例使用 |
| 自动装配构造函数 | `b.Provide[Repository](NewSqlRepository)` | 任意签名 `func(depA, depB, ...) T` 或 `(T, error)`；每个参数类型都必须是另一个已注册的服务，`Build()` 时递归解析并按依赖顺序调用 |

**注意事项**：

- factory 函数可以返回 `(T)` 或 `(T, error)`；`T` 必须能赋值给注册类型（相同类型或实现了该接口）。
- **不支持手动工厂**（在函数体内自行调用 `Get` 的写法）：构造函数只能通过参数类型静态声明依赖；需要"函数体内动态取值"时注入 `Provider[T]` / `Keyed[T]`，见 [第 12 节](#12-动态解析providert-与-keyedt)。
- **签名不合法（返回值个数不对、返回类型不满足接口等）会在注册时直接 `panic`**——配置期错误，尽早暴露。

---

### 4. 解析服务

```go
app, err := b.Build() // 所有单例已在这里构造完成

logger, err := app.Get[Logger]()           // 返回 (T, error)
logger := app.MustGet[Logger]()            // 出错直接 panic
validators, err := app.GetAll[Validator]() // 按注册顺序返回全部实现
```

- 未注册的服务返回 `kernel.ErrServiceNotRegistered`（`errors.Is` 可判断）。
- `Build()` 之后解析只是 map/切片查找，不再触发任何构造、反射或加锁。

---

### 5. 生命周期：Start / Stop / Close

需要"启动阶段"（监听端口、拉起 worker、连接外部服务）或"清理阶段"（关连接、flush 缓冲）的单例，实现对应接口即可，调用时机由 App 统一管理：

```go
type Server struct {
    srv *http.Server
    cfg *kernel.Config[AppConfig]
}

func NewServer(cfg *kernel.Config[AppConfig]) *Server {
    return &Server{cfg: cfg, srv: &http.Server{Addr: fmt.Sprintf(":%d", cfg.Value.Port)}}
}

// Starter：App.Start 按构造顺序调用
func (s *Server) Start(ctx context.Context) error {
    go s.srv.ListenAndServe()
    return nil
}

// Stopper：App.Stop 按构造逆序调用，可基于 ctx 做超时控制
func (s *Server) Stop(ctx context.Context) error {
    return s.srv.Shutdown(ctx) // 等待在途请求处理完
}

// 没有 ctx 需求的清理逻辑：实现 io.Closer，注册时声明 WithClose
type ConnPool struct{ db *sql.DB }

func NewConnPool() *ConnPool { return &ConnPool{db: openDB()} }

func (p *ConnPool) Close() error { return p.db.Close() }
```

main 里的完整用法——注册、Build、一键优雅退出：

```go
func main() {
    app, err := kernel.New().
        Provide[*Server](NewServer).
        Provide[*ConnPool](NewConnPool, kernel.WithClose()).
        Build()
    if err != nil {
        panic(err)
    }

    if err := app.RunUntilSignal(); err != nil { // 等 SIGINT/SIGTERM → 15s 超时优雅关闭
        slog.Error("shutdown failed", "error", err)
    }
}
```

- **调用顺序**：`App.Start` 按**构造顺序**调用所有 `Starter`；`App.Stop` 按**构造逆序**关闭所有 `Stopper` 与 `WithClose` 标记的 `io.Closer`（依赖方先于被依赖方停，避免先关掉底层的 DB 连接池）。同时实现 `Stopper` 与 `io.Closer` 时只调 `Stop`（`Close` 被忽略）。
- **一键优雅退出**：`app.RunUntilSignal()` = 等待 SIGINT/SIGTERM → 15 秒超时优雅关闭（关闭进行中再收到一次信号则立即强制退出）；需要自定义取消源时用 `app.Run(ctx)`（K8s、测试、自定义信号集场景）。两者只是 `Start`/`Stop` 的编排，与手动路径幂等兼容；`Run` 返回后 App 不可复用。
- **`io.Closer` 默认被忽略**：`Close() error` 是 Go 生态最泛滥的签名，嵌入 `*sql.DB`/`*os.File` 会无意满足该接口，因此只有注册时显式声明 `kernel.WithClose()` 的单例才会在 `Stop` 时被 `Close`；`Stopper` 始终自动识别（其签名足够特异，不易误伤）。
- **失败语义**：`Start` 在第一个失败的 `Start` 处停止，后续服务不再启动，错误包装了失败服务的类型；`Stop` 会尝试关闭**每一个**服务，所有错误经 `errors.Join` 聚合后一并返回。
- **幂等**：`Start`/`Stop` 重复调用安全，只有第一次生效；`app.Close()` 等价于 `app.Stop(context.Background())`，App 本身满足 `io.Closer`。
- WithReloadable 配置的 watcher 会在 `Stop` 时**先于服务**一并停止。
- 未实现任何生命周期接口的服务不受影响，`Start`/`Stop` 都不会碰它们。

---

### 6. TryProvide 系列

```go
b.Provide[Logger](NewConsoleLogger)
b.TryProvide[Logger](NewFileLogger) // Logger 已注册过，本次调用被跳过
```

- `TryProvide` 只在 `T` **完全没有任何注册**时才会真正注册。
- `TryProvideKeyed[T](key, factory)` 同理，判断维度是 `(T, key)` 组合。
- 典型用途：扩展函数内部用 `TryProvide` 保证重复调用幂等，见 [第 11 节](#11-扩展机制extend)。

---

### 7. Options 模式：Configure

```go
type AppConfig struct{ Port int }

b.Configure[AppConfig](func(c *AppConfig) { c.Port = 8080 })
b.Configure[AppConfig](func(c *AppConfig) { c.Port++ }) // 多次 Configure 按注册顺序依次应用

app, _ := b.Build()
fmt.Println(app.MustGet[*kernel.Config[AppConfig]]().Value.Port) // 8081
```

- `Configure[T]` 产出只读的 `*Config[T]{Value: T}`（Singleton）。
- `ConfigureMonitor[T]` 产出可变的 `*ConfigMonitor[T]`：`CurrentValue()` 无锁读取、`OnChange(listener)` 订阅（返回退订函数）、`Set(v)` 手动推送。监听器调用不持锁，可安全地再次 `OnChange`/`Set`。
- 与文件配置的关系见下一节：`Config[T]` 是"从源加载"的方式，`Configure[T]` 是"用代码配置"的方式，两者产出同一个 `*Config[T]` 类型。

---

### 8. 配置：Config[T]

内置配置选项位于 `kernel/config` 子包（`config.WithDefaults` / `config.WithFile` / `config.WithEnv` ...）：

```go
import (
    "github.com/gocrud/kernel"
    config "github.com/gocrud/kernel/config"
)

b.Config[AppConfig]("app",                     // 类型 + section 一次指定
    config.WithDefaults(func(c *AppConfig) { c.Port = 8080 }), // 强类型默认值（最低优先级）
    config.WithFile("./config/app.yaml"),          // .json/.yaml/.yml/.toml 自动识别
    config.WithEnv("APP_"),                        // APP_PORT → app.port
    config.WithFlag(flag.CommandLine),             // 已设置的 flag
)

app, _ := b.Build()
cfg := app.MustGet[*kernel.Config[AppConfig]]()
fmt.Println(cfg.Value.Port)
```

- **一步式**：`b.Config[T](section, opts...)` 把"多源合并 → section 绑定 → 注册 `*Config[T]`"合并为一次调用。`Build()` 时加载，加载/解析/绑定错误并入 `Build()` 的错误返回（fail-fast）。
- **配置源与优先级**：所有源先被扁平化为 dotted key（`app.port`），按**注册顺序**合并——同 key 后者覆盖前者，不同 key 互不干扰。推荐顺序 `WithDefaults < WithFile < WithMap < WithEnv < WithFlag`：

  | 选项 | 说明 |
  | --- | --- |
  | `config.WithDefaults(func(*T)...)` | 强类型默认值；源未提供的 key 保留默认 |
  | `config.WithFile(path)` | 按扩展名选 codec（JSON/YAML/TOML） |
  | `config.WithMap(map[string]any)` | 内存补充值 |
  | `config.WithEnv(prefix)` | 环境变量，`APP_PORT` → `app.port`；prefix 只过滤不参与映射 |
  | `config.WithFlag(fs)` | 已设置 flag 的值，flag 名即 key（可含 `.`） |
  | `config.WithSource(src)` | 任意自定义源（实现 `kernel.Source`；再实现 `kernel.WatchSource` 即可支持推送重载） |

- **绑定规则**：嵌套 struct / map / slice / 标量；字段 tag 优先级 `config:"app.port"` → `json` → 字段名 snake_case；字符串→int/bool/Duration 等转换统一在绑定层完成（所以 YAML 里的 `int` 与 env 里的字符串都能绑定到同一字段）；错误信息带完整 key 路径。
- **多文件/多源不冲突**：合并是确定性的 last-wins；文件删掉的 key 重载后即消失；同名 key 结构冲突（一处 map、一处标量）或转换失败在 `Build()` 时报 `*kernel.ConfigError`。
- 同一个文件可以被多个 `Config` 绑定不同 section；同一个 section 也可以绑定到不同的 struct。
- **脱离 DI 使用**：`kernel.LoadConfig[T](section, opts...) (*T, error)` 一次性静态加载并绑定。
- 必须在 `Build()` 之前调用 `Config`（包级 `kernel.LoadConfig` 无此限制）。

---

### 9. 热重载：WithReloadable

```go
b.Config[AppConfig]("app",
    config.WithFile("./config/app.yaml"),
    config.WithReloadable,                          // 开启动态（默认关闭）
    config.WithOnReloadError(func(err error) {      // 可选：重载失败回调
        slog.Error("config reload failed", "error", err)
    }),
)

app, _ := b.Build()
mon := app.MustGet[*kernel.ConfigMonitor[AppConfig]]()

// 文件变化 → fsnotify → debounce → 从零重建合并 → 绑定 → 自动 Set，业务无感知：
// CurrentValue() 永远是最新值；想感知变化可订阅：
cancel := mon.OnChange(func(cfg AppConfig) { fmt.Println("new port:", cfg.Port) })
defer cancel()
```

- `WithReloadable` 是**默认关闭的可选项**：不加时 `b.Config[T]` 只注册静态 `*Config[T]`，零后台开销。
- 开启后额外注册 `*ConfigMonitor[T]`（初值与静态一致）：
  - **文件源**用 fsnotify 监控（watch 父目录，兼容 vim/sed -i 的原子替换保存），debounce 合并连续事件；
  - 实现 `WatchSource` 的源走推送（核心不内建此类源，第三方可实现后经 `config.WithSource` 接入）。
- 重载失败**保留旧值**，错误走 slog（Error 级）+ 可选 `WithOnReloadError`。
- watcher 在 `Build()` 成功后自动启动，随 App `Stop`/`Close` 停止。

---

### 10. Decorate 装饰器

```go
b.Provide[Logger](NewConsoleLogger)
b.Decorate[Logger](func(inner Logger) (Logger, error) {
    return &timestampLogger{inner: inner}, nil
})
```

- `Decorate[T]` 包装 `T` **最后一次注册**的实现；decorator 的第一个参数是原始实例，之后可声明任意额外依赖（自动装配，计入依赖图与循环检测）。
- 多次 `Decorate` 形成装饰链，越晚调用越靠外层。
- `T` 必须已注册，否则注册时直接 `panic`。

---

### 11. 扩展机制：Extend

```go
// mypkg/httpclient.go —— 可复用注册扩展
func AddHttpClient(b *kernel.AppBuilder) {
    b.TryProvide[*http.Client](func() *http.Client {
        return &http.Client{Timeout: 30 * time.Second}
    })
}

// 使用方
b.Provide[Logger](NewConsoleLogger).
    Extend(mypkg.AddHttpClient).
    Provide[*Repository](NewRepository)
```

- `type Extension func(*AppBuilder)`；扩展函数只做注册、无返回值，`b.Extend(ext)` 调用 `ext(b)` 后返回 `b` 本身，因此仍可继续链式调用。
- 约定：扩展函数内部用 `TryProvide` 保证幂等；只使用公开 API 即可实现。

---

### 12. 动态解析：Provider[T] 与 Keyed[T]

框架不支持"构造函数拿到 App 自己 `Get`"的手动工厂；取而代之的是两个**可注入的访问器**，它们本身是框架自动注册的单例（`TryProvide` 语义，用户注册优先）：

```go
// Provider[T]：非 keyed 空间。Get 是 O(1) 查表，无反射无锁。
b.Provide[Handler](func(cfg Provider[AppConfig]) *Handler {
    return &Handler{cfg: cfg.MustGet()} // 构造期内即可安全取值
})

// 或延迟到运行时再取（依赖边保证 AppConfig 先构造）：
b.Provide[Dispatcher](func(p Provider[Handler]) *Dispatcher {
    return &Dispatcher{newHandler: p} // p.Get() 随时可用
})
```

```go
// Keyed[T]：keyed 空间。key 是运行时值，可在函数体内按配置决定。
b.ProvideKeyed[Cache]("redis", NewRedisCache).
    ProvideKeyed[Cache]("memory", NewMemoryCache).
    Provide[Router](func(k Keyed[Cache], opts *kernel.Config[AppConfig]) *Router {
        return &Router{cache: k.MustGet(opts.Value.CacheDriver)}
    })
```

- `Provider[T]`：`Get() (T, error)` / `MustGet() T`。
- `Keyed[T]`：`Get(key any) (T, error)` / `MustGet(key any) T` / `All(key any) []T`。
- 两者的依赖边都计入依赖图：`Provider[X] → X`、`Keyed[X] → X 的全部 keyed 注册`，因此构造期内取值永远安全、循环依赖照常被检测。

---

### 13. Keyed Services 具名服务

```go
b.ProvideKeyed[Cache]("redis", NewRedisCache).
  ProvideKeyed[Cache]("memory", NewMemoryCache)

app, _ := b.Build()

redis := app.MustGetKeyed[Cache]("redis")
memory, err := app.GetKeyed[Cache]("memory")
all := app.GetAllKeyed[Cache]("redis") // 该 key 下全部注册，未知 key 返回空切片
```

- `key` 必须可比较（string/int/枚举等）。
- **Keyed 与非 Keyed 是两套独立空间**：keyed 注册只能通过 `GetKeyed` 系列（或注入 `Keyed[T]`）访问，反之亦然。
- 未注册的 `(T, key)`：`GetKeyed` 返回 `ErrServiceNotRegistered`；`MustGetKeyed` panic；`GetAllKeyed` 返回空切片。
- `Build()` 同时校验 keyed 与非 keyed 的全部注册项。

---

### 14. Build：依赖图、循环依赖检测与诊断

`Build()` 内部：分配下标（参数类型静态解析为依赖下标）→ 建图 + DFS 循环检测 → 拓扑排序 → 按序构造（构造后应用 Decorate 链、登记生命周期）。只要 `Build()` 成功返回，所有服务都已无错误地构造完成。

```go
type A struct{ B *B }
type B struct{ A *A }

_, err := kernel.New().
    Provide[*A](func(bb *B) *A { return &A{B: bb} }).
    Provide[*B](func(aa *A) *B { return &B{A: aa} }).
    Build()
// 返回 *kernel.CircularDependencyError，不会等到 Get 才发现
```

**诊断**：

- `app.Describe(w)`：树形文本——每个服务的形态（instance/constructor/provider/keyed-accessor）、key、装饰链、依赖边、构造耗时。
- `app.Dot(w)`：Graphviz DOT 格式的依赖图。

---

### 15. 错误处理

```go
app, err := b.Build()
if err != nil {
    var circ *kernel.CircularDependencyError
    var cfgErr *kernel.ConfigError
    switch {
    case errors.Is(err, kernel.ErrServiceNotRegistered):
        // 构造函数/decorator 依赖了未注册的类型
    case errors.As(err, &circ):
        // circ.Chain 是完整依赖链，Error() 形如
        // "kernel: circular dependency detected: *main.A -> *main.B -> *main.A"
    case errors.As(err, &cfgErr):
        // 配置加载/解析/绑定失败，cfgErr.Source 指示来源
    default:
        // 构造函数/decorator 自身返回的 error（已包装服务类型信息）
    }
    panic(err)
}
```

- 所有"未注册"错误都包装 `ErrServiceNotRegistered`，`errors.Is` 可判断。
- 注册期编程错误（非法 factory 形态、签名不合法、`Decorate` 目标未注册）**直接 `panic`**。
- 热重载失败不返回错误：保留旧值，走 slog + `WithOnReloadError`。

---

### 16. 完整示例

```go
package main

import (
    "context"
    "flag"
    "fmt"
    "log/slog"
    "net/http"

    "github.com/gocrud/kernel"
    config "github.com/gocrud/kernel/config"
)

type AppConfig struct {
    Port int
}

type Server struct{ Cfg *kernel.Config[AppConfig] }

func NewServer(cfg *kernel.Config[AppConfig]) *Server { return &Server{Cfg: cfg} }

func (s *Server) Start(ctx context.Context) error {
    addr := fmt.Sprintf(":%d", s.Cfg.Value.Port)
    go http.ListenAndServe(addr, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        fmt.Fprintln(w, "ok")
    }))
    slog.Info("server started", "addr", addr)
    return nil
}

func main() {
    app, err := kernel.New().
        WithLogger(slog.Default()).
        Config[AppConfig]("app",
            config.WithDefaults(func(c *AppConfig) { c.Port = 8080 }),
            config.WithFile("./config/app.yaml"),
            config.WithEnv("APP_"),
            config.WithFlag(flag.CommandLine),
            config.WithReloadable, // 修改 app.yaml 后自动生效
        ).
        Provide[*Server](NewServer).
        Build()
    if err != nil {
        panic(err)
    }
    mon := app.MustGet[*kernel.ConfigMonitor[AppConfig]]()
    mon.OnChange(func(cfg AppConfig) { slog.Info("config changed", "port", cfg.Port) })

    if err := app.RunUntilSignal(); err != nil { // 等信号 → 优雅退出（15s 超时）
        slog.Error("shutdown failed", "error", err)
    }
}
```

---

### 17. 已知限制

- 只支持 Singleton：没有 Scoped/Transient；"每次请求一份"由业务代码在 Singleton 之上自行创建。
- 构造函数不能拿到 App 本身：动态解析统一通过注入 `Provider[T]` / `Keyed[T]` 完成，keyed 依赖无法按编译期 key 注入（Go 泛型不接受值参数；编译期 marker 类型方案列 future）。
- 文件监控依赖 fsnotify；Windows/Linux/macOS 均支持，NFS 等不产生文件事件的场景不保证实时。
- 配置源仅内建 file/env/flag/map（选项函数位于 `kernel/config` 包）；其他源请实现 `kernel.Source`（可选的 `kernel.WatchSource`）经 `config.WithSource` 接入。
