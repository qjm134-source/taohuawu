
# Go 编程规范与最佳实践

> 基于 Google Go Style Guide、Effective Go 及社区最佳实践整理
> 适用于面试项目代码规范化改造

---

## 一、核心原则（按优先级排序）

1. **清晰性（Clarity）**：代码意图和原理对读者清晰
2. **简洁性（Simplicity）**：用最简单的方式实现目标
3. **精炼性（Concision）**：高信噪比，减少噪音
4. **可维护性（Maintainability）**：易于后续修改
5. **一致性（Consistency）**：与更广泛的代码库保持一致

---

## 二、代码格式化

### 2.1 强制使用 gofmt
- 所有 Go 源文件必须通过 `gofmt` 格式化
- 使用 `goimports` 自动管理 import（标准库 → 第三方 → 本地包 → 空导入）
- 生成代码也应格式化

### 2.2 基本格式规则
- 使用 Tab 缩进
- 左花括号 `{` 不换行
- 无固定行长度限制；若一行过长，优先重构而非强行拆分
- 不要在缩进变化前（如函数声明、条件语句前）拆行

---

## 三、命名规范

### 3.1 命名风格
- 使用 `MixedCaps`（驼峰式），不使用下划线（snake_case）
- 包名：全小写，简短有意义，避免 `util`、`common`、`helper` 等无信息名称
- 导出标识符：首字母大写（CamelCase）
- 未导出标识符：首字母小写（camelCase）

### 3.2 命名长度原则
- 名称长度与作用域大小成正比，与使用频率成反比
- 小作用域（1-7行）：可用短名，如 `i`、`c`、`err`
- 大作用域（>25行）：需更具描述性的名称

### 3.3 具体命名规则

| 类型 | 规则 | 示例 |
|------|------|------|
| 包名 | 全小写，无下划线 | `httputil`、`tabwriter` |
| 接口 | 单方法接口加 `-er` 后缀 | `Reader`、`Writer` |
| 常量 | MixedCaps，不用 `K` 前缀或全大写 | `MaxRetryCount` |
| 接收者 | 1-2字母缩写，类型一致 | `(t *Tray)`、`(w *ReportWriter)` |
| Getter | 不加 `Get` 前缀 | `Counts()` 而非 `GetCounts()` |
| 初始词 | 保持大小写一致 | `URL`、`ID`、`HTTPClient` |

### 3.4 避免重复命名
- 包名与导出符号不重复：`widget.New` 而非 `widget.NewWidget`
- 不重复类型信息：`users` 而非 `userSlice`
- 不重复上下文：`db.Load` 而非 `db.LoadFromDatabase`

---

## 四、包设计

### 4.1 包组织原则
- 包名应反映其提供的内容
- 避免无意义的 `util`、`helper`、`common` 包
- 接口应在消费者包中定义，而非实现包
- 包大小适中：不宜一个包包含整个项目，也不宜一个类型一个文件

### 4.2 导入规范
- 导入分组（按顺序）：标准库 → 第三方 → 本地包 → 空导入（副作用）
- 避免使用 `import .`（点导入）
- 空导入（`import _`）仅在 main 包或测试中使用
- 需要重命名时保持一致，避免下划线和大写字母

### 4.3 包注释
- 每个包一个包注释，紧跟在 `package` 子句上方，无空行
- 以 "Package xxx" 开头，完整句子
- 长注释可放在 `doc.go` 中

---

## 五、错误处理

### 5.1 基本规则
- 函数返回 `error` 作为最后一个返回值
- 不要忽略错误；若确实要丢弃，用 `_` 并注释说明
- 不使用 panic 进行正常错误处理
- 使用 `fmt.Errorf` 包装错误，保留错误链

### 5.2 错误包装
- `%w`：保留原始错误供 `errors.Is`/`errors.As` 检查（适合需要程序判断的场景）
- `%v`：仅添加人类可读上下文（适合日志展示，不保留结构）
- `%w` 通常放在错误字符串末尾：`fmt.Errorf("...: %w", err)`
- 哨兵错误（sentinel）可放在开头：`fmt.Errorf("%w: invalid header", ErrParse)`

### 5.3 错误处理风格
```go
// 好：先处理错误，减少嵌套
if err != nil {
    return fmt.Errorf("reading config: %w", err)
}
// 正常逻辑...

// 好：错误字符串小写，无结尾标点
err := fmt.Errorf("something bad happened")

// 好：使用结构化错误而非字符串匹配
if errors.Is(err, ErrNotFound) { ... }
```

### 5.4 避免内联错误（In-band Errors）
- 不要用特殊返回值（如 -1、nil）表示错误
- 使用多返回值：`(value, ok bool)` 或 `(value, error)`

---

## 六、函数与方法设计

### 6.1 函数签名
- 保持简洁，避免过多参数
- 参数过多时考虑使用选项结构体或变长选项模式
- 函数返回值：error 总是最后一个

### 6.2 接收者选择
| 场景 | 接收者类型 |
|------|-----------|
| 不修改接收者 | 值接收者 |
| 需要修改接收者 | 指针接收者 |
| 包含 `sync.Mutex` 等不可复制字段 | 指针接收者 |
| 大结构体或数组 | 指针接收者（效率考虑） |
| 内置类型（int、string）且不修改 | 值接收者 |
| map、function、channel | 值接收者 |
| 不确定时 | 指针接收者 |

- 一个类型的方法尽量统一使用指针或值接收者

### 6.3 参数传递
- 不要仅为省几个字节就传指针
- 字符串、接口值直接传值，不要传 `*string`、`*io.Reader`
- Protocol Buffer 消息通常传指针

### 6.4 返回值命名
- 仅在需要文档说明或延迟闭包中修改时使用命名返回值
- 不要仅为避免声明变量而命名返回值
- 短函数可用 naked return，中等以上函数显式返回

---

## 七、并发编程

### 7.1 Goroutine 生命周期
- 启动 goroutine 时必须清楚它何时/如何退出
- 使用 `sync.WaitGroup` 等待 goroutine 完成
- 使用 `context.Context` 管理生命周期和取消信号
- 避免 goroutine 泄漏（阻塞在 channel 上）

### 7.2 Channel 使用
- 尽可能指定 channel 方向（`<-chan`、`chan<-`）
- 优先使用同步函数，由调用者决定是否需要并发
- 发送已关闭的 channel 会导致 panic
- **Channel 的 size 要么是 1，要么是无缓冲的**：避免任意大小的缓冲 channel，容易导致不可预测的行为
- select 语句中必须包含 default 分支或设置超时（`time.After`），杜绝无限期挂起

```go
// Bad
ch := make(chan int, 100)  // 任意大小容易导致不可预测的行为

// Good
ch := make(chan int)     // 无缓冲
ch := make(chan int, 1)  // 或 size=1，用于解耦生产者和消费者
```

### 7.3 Mutex 使用
- **零值的 `sync.Mutex` 和 `sync.RWMutex` 是有效的**，不需要指向 Mutex 的指针
- 对于导出类型，将 mutex 作为私有成员变量；对于非导出类型，可嵌入结构体
- 强制使用 `defer mu.Unlock()` 模式防止死锁

```go
// Bad
mu := new(sync.Mutex)
mu.Lock()
// ... 忘记解锁会导致死锁

// Good
var mu sync.Mutex  // 零值即可用
mu.Lock()
defer mu.Unlock()  // 确保一定会解锁
```

### 7.4 Context 使用
- `context.Context` 作为函数第一个参数
- 不要在 struct 中存储 context，作为参数传递
- 不要创建自定义 context 类型
- 使用 `context.WithTimeout`、`context.WithCancel` 等创建派生 context

---

## 八、接口设计

### 8.1 避免过早抽象
- 不要仅为"服务"或"仓库"等概念创建接口
- 不要在 RPC 客户端上手动包装接口
- 不要仅为测试导出测试替身实现

### 8.2 接口设计原则
- 接口由消费者定义（使用方定义需要什么方法）
- 保持接口小巧（方法少）
- 接受接口，返回具体类型
- 仅在以下情况创建接口：
  - 有多个实现需要统一处理
  - 需要解耦包依赖（打破循环依赖）
  - 需要隐藏复杂实现细节

### 8.3 指向接口的指针
- 几乎不需要指向接口类型的指针，直接将接口作为值传递即可
- 接口在底层包含两个字段：类型指针和数据指针，传递接口值时底层数据仍然可以是指针
- 如果需要修改底层数据，使用指针类型的方法即可

```go
// Bad
var reader *io.Reader

// Good
var reader io.Reader  // 接口本身就是指针语义，直接传值
```

---

## 九、测试规范

### 9.1 测试框架
- 仅使用标准库 `testing` 包
- 不使用断言库（assertion libraries）
- 使用 `cmp` 包进行复杂结构比较

### 9.2 测试结构
- 优先使用表驱动测试（table-driven tests）
- 使用子测试（`t.Run`）组织相关用例
- 测试失败信息格式：`YourFunc(%v) = %v, want %v`
- 使用 `got` 和 `want` 而非 `actual` 和 `expected`

### 9.3 测试辅助函数
- 测试辅助函数（setup/cleanup）调用 `t.Helper()`
- 辅助函数返回 error 而非直接调用 `t.Fatal`
- 不要在非测试 goroutine 中调用 `t.Fatal`

### 9.4 测试最佳实践
- 测试应尽可能继续执行（`t.Error` 优于 `t.Fatal`）
- 使用 `t.Cleanup` 注册清理函数
- 保持测试资源作用域最小化
- 比较结构体时使用 `cmp.Diff` 而非手写字段比较

---

## 十、文档注释

### 10.1 注释规范
- 所有顶层导出名称必须有文档注释
- 注释以被描述对象的名称开头，完整句子
- 注释解释"为什么"而非"做什么"（代码本身应说明做什么）
- 包注释放在 `doc.go` 或主要文件中

### 10.2 注释格式
- 段落间空一行
- 代码示例缩进两个空格
- 可运行示例放在 `*_test.go` 中

---

## 十一、其他重要规范

### 11.1 字面量格式化
- 跨包类型字面量必须指定字段名
- 本包类型可选字段名，但字段多时建议指定
- 零值字段可省略
- 关闭花括号与开启花括号保持相同缩进级别

### 11.2 字符串处理
- 简单拼接用 `+`
- 复杂格式化用 `fmt.Sprintf`
- 逐步构建用 `strings.Builder`
- 多行常量字符串用反引号 `` ` ``

### 11.3 避免全局状态
- 库代码不应依赖全局状态
- 使用依赖注入传递实例
- 如需全局实例，提供 `New()` 构造函数，全局 API 作为薄包装

### 11.4 切片与 Map 的边界拷贝
切片和 map 包含指向底层数据的指针，传递或存储引用时需特别注意防止外部修改内部状态。

#### 接收切片和 Map
```go
// Bad
func (d *Driver) SetTrips(trips []Trip) {
    d.trips = trips  // 浅拷贝，外部修改会影响内部状态
}

// Good
func (d *Driver) SetTrips(trips []Trip) {
    d.trips = make([]Trip, len(trips))
    copy(d.trips, trips)  // 深拷贝，隔离外部影响
}
```

#### 返回切片和 Map
```go
// Bad
type Stats struct {
    counters map[string]int
}

func (s *Stats) Counters() map[string]int {
    return s.counters  // 直接返回内部状态，调用者可修改
}

// Good
func (s *Stats) Counters() map[string]int {
    result := make(map[string]int, len(s.counters))
    for k, v := range s.counters {
        result[k] = v
    }
    return result  // 返回副本，保护内部状态
}
```

### 11.5 空切片声明
```go
// Bad
s := []int{}  // 分配了内存（虽然很小）

// Good
var s []int  // nil 切片，不分配内存
// nil 切片可以安全地用于 range、len、append 等操作
```

### 11.6 泛型使用
- 仅在真正需要时使用泛型
- 不要仅为实现通用算法而使用泛型
- 如果只有一种类型实例化，先写非泛型版本

### 11.7 枚举常量从 1 开始
- 使用 `iota` 定义枚举时，从 1 开始，将零值保留给"未设置"状态
- 零值应代表无效或默认状态

```go
// Bad
const (
    Red = iota  // 0，可能与未设置状态混淆
    Green
    Blue
)

// Good
const (
    Red = iota + 1  // 从1开始
    Green
    Blue
)

// 零值保留给未设置
const (
    StatusUnknown Status = iota
    StatusActive
    StatusInactive
)
```

### 11.8 时间处理
- 使用 `time.Time` 表达瞬时时间（如创建时间、过期时间）
- 使用 `time.Duration` 表达时间段（如超时时间、延迟时间）
- 对外部系统（API、数据库）使用 `time.Time` 和 `time.Duration`，保持一致性
- 避免使用 int64 毫秒时间戳作为时间表达

```go
// Bad
func DoSomething(timeoutMs int64) error  // 使用毫秒数

// Good
func DoSomething(timeout time.Duration) error  // 使用 time.Duration
```

### 11.9 类型断言失败处理
- 使用 comma-ok 模式处理类型断言，避免 panic
- 不要直接使用断言，始终检查结果

```go
// Bad
val := m["key"].(string)  // 类型不匹配会 panic

// Good
val, ok := m["key"].(string)
if !ok {
    return fmt.Errorf("expected string, got %T", m["key"])
}
```

### 11.10 使用 defer 释放资源
- 使用 `defer` 确保资源在函数退出时被释放
- 适用于文件句柄、锁、网络连接等资源
- defer 的执行顺序是后进先出（LIFO）

```go
func ReadFile(path string) ([]byte, error) {
    f, err := os.Open(path)
    if err != nil {
        return nil, fmt.Errorf("opening file: %w", err)
    }
    defer f.Close()  // 确保文件关闭

    data, err := io.ReadAll(f)
    if err != nil {
        return nil, fmt.Errorf("reading file: %w", err)
    }
    return data, nil
}
```

---

## 十二、常用工具链

```bash
# 格式化
go fmt ./...
goimports -w *.go

# 静态检查
go vet ./...
golangci-lint run

# 测试
go test ./...
go test -race ./...

# 生成与检查
go generate ./...
```

---

## 参考文档

- [Google Go Style Guide](https://google.github.io/styleguide/go/guide)
- [Effective Go](https://go.dev/doc/effective_go)
- [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)
- [Uber Go Style Guide](https://github.com/uber-go/guide/blob/master/style.md)


---

---

## 十三、代码质量与可读性规范

> 本章节补充函数长度、重复代码抽象、圈复杂度等代码质量层面的规范，与风格规范共同构成完整的编程规范体系。

### 13.1 函数长度限制

函数长度直接影响可读性和可维护性。Go 社区和 Google 内部虽无绝对硬性行数限制，但遵循以下原则：

| 级别 | 建议行数 | 处理方式 |
|------|----------|----------|
| **理想** | ≤ 30 行 | 一个屏幕能看完，逻辑一目了然 |
| **可接受** | ≤ 50 行 | 包含错误处理和简单分支 |
| **警告** | > 50 行 | 需要考虑拆分 |
| **必须拆分** | > 80-100 行 | 几乎一定需要重构 |

**核心原则：一个函数只做一件事（Single Responsibility）。**

#### 拆分信号

出现以下任一情况时，函数应考虑拆分：
- 函数内部出现明显的"段落"（用空行分隔的逻辑块）
- 需要写注释解释"这段代码在做什么"
- 嵌套层级超过 3-4 层
- 需要滚动屏幕才能看完整个函数
- 函数名无法简洁描述其全部职责

#### 拆分方法

```go
// 坏：一个函数做太多事（假设有 80+ 行）
func ProcessOrder(orderID string) error {
    // 1. 验证订单（20行）
    // 2. 扣减库存（20行）
    // 3. 生成物流单（20行）
    // 4. 发送通知（20行）
}

// 好：拆分为职责单一的函数
func ProcessOrder(orderID string) error {
    order, err := validateOrder(orderID)
    if err != nil {
        return fmt.Errorf("validating order: %w", err)
    }
    if err := deductInventory(order); err != nil {
        return fmt.Errorf("deducting inventory: %w", err)
    }
    if err := createShipment(order); err != nil {
        return fmt.Errorf("creating shipment: %w", err)
    }
    if err := sendNotification(order); err != nil {
        return fmt.Errorf("sending notification: %w", err)
    }
    return nil
}
```

### 13.2 重复代码抽象（DRY 原则）

Don't Repeat Yourself。重复代码是维护的噩梦，修改一处需要同步修改多处。

| 出现次数 | 处理方式 | 紧迫程度 |
|----------|----------|----------|
| 2 次 | 考虑提取为辅助函数 | 建议 |
| 3 次+ | **必须**提取为公共函数 | 强制 |
| 仅参数不同 | 提取为带参数的函数 | 强制 |
| 仅类型不同 | 考虑使用泛型或接口 | 建议 |

#### 提取原则

- **提取的函数应有明确语义**：函数名能说明其用途
- **不要过度抽象**：如果提取后反而增加理解成本，保持内联
- **注意性能敏感代码**：抽象可能带来微小开销，在热路径上谨慎

```go
// 坏：重复的错误处理模式
func CreateUser(name string) error {
    if name == "" {
        return fmt.Errorf("name is required")
    }
    // ...
}

func CreateProduct(name string) error {
    if name == "" {
        return fmt.Errorf("name is required")
    }
    // ...
}

// 好：提取为验证函数
func validateNotEmpty(field, value string) error {
    if value == "" {
        return fmt.Errorf("%s is required", field)
    }
    return nil
}

func CreateUser(name string) error {
    if err := validateNotEmpty("name", name); err != nil {
        return err
    }
    // ...
}
```

#### Go 特有的抽象方式

| 模式 | 适用场景 | 示例 |
|------|----------|------|
| **表驱动测试** | 消除测试中的重复逻辑 | `tests := []struct{...}{}` |
| **函数选项模式** | 消除构造函数参数膨胀 | `NewServer(WithPort(8080))` |
| **闭包** | 回调和延迟执行的模式 | `http.HandlerFunc` |
| **接口组合** | 多个小接口合成大接口 | `io.ReadWriter` |

### 13.3 圈复杂度（Cyclomatic Complexity）

圈复杂度衡量代码路径数量，复杂度越高，测试和维护越困难。

| 复杂度 | 含义 | 建议 |
|--------|------|------|
| 1-10 | 简单 | ✅ 良好 |
| 11-20 | 较复杂 | ⚠️ 考虑简化 |
| 21-50 | 复杂 | ❌ 需要重构 |
| > 50 | 极复杂 | 🚨 必须拆分 |

**计算方法**：每个 `if`、`for`、`switch` 的 case、`&&`、`||` 等分支点都会增加复杂度。

#### 降低复杂度的方法

```go
// 坏：深层嵌套，高复杂度
func process(data []Item) error {
    for _, item := range data {
        if item.Valid {
            if item.Type == "A" {
                if item.Value > 0 {
                    // 处理逻辑
                } else {
                    return fmt.Errorf("invalid value")
                }
            } else if item.Type == "B" {
                // 更多嵌套...
            }
        }
    }
    return nil
}

// 好：提前返回 + 提取函数
func process(data []Item) error {
    for _, item := range data {
        if err := processItem(item); err != nil {
            return fmt.Errorf("processing item %s: %w", item.ID, err)
        }
    }
    return nil
}

func processItem(item Item) error {
    if !item.Valid {
        return nil // 跳过无效项
    }
    switch item.Type {
    case "A":
        return processTypeA(item)
    case "B":
        return processTypeB(item)
    default:
        return fmt.Errorf("unknown type: %s", item.Type)
    }
}
```

#### 常用简化技巧

1. **提前返回（Guard Clauses）**：将无效条件提前处理，减少嵌套
2. **提取命名布尔变量**：将复杂条件赋予语义化名称
3. **提取 switch case**：每个 case 逻辑独立成函数
4. **策略模式**：用 map 替代长 if-else/switch 链
5. **多态**：用接口的不同实现替代类型判断

### 13.4 嵌套层级控制

| 嵌套层级 | 评价 | 建议 |
|----------|------|------|
| 1-2 层 | 良好 | ✅ 保持 |
| 3 层 | 可接受 | ⚠️ 注意简化 |
| 4 层+ | 过深 | ❌ 必须重构 |

**Go 惯用法：错误处理优先，正常逻辑左对齐。**

```go
// 坏：正常逻辑深嵌套在 if 内部
if err == nil {
    result, err := process(data)
    if err == nil {
        if result.Valid {
            return result.Value, nil
        }
    }
}

// 好：提前返回，正常逻辑在顶层
if err != nil {
    return 0, fmt.Errorf("initial check: %w", err)
}
result, err := process(data)
if err != nil {
    return 0, fmt.Errorf("processing: %w", err)
}
if !result.Valid {
    return 0, fmt.Errorf("invalid result")
}
return result.Value, nil
```

### 13.5 参数与返回值数量

| 项目 | 建议上限 | 处理方式 |
|------|----------|----------|
| **函数参数** | 4-5 个 | 超过用选项结构体或变长选项 |
| **返回值** | 3 个 | 超过用结构体包装 |

```go
// 坏：参数过多
func NewServer(host string, port int, timeout time.Duration, maxConns int, tlsConfig *tls.Config, logger *Logger) (*Server, error)

// 好：使用选项结构体
type ServerOptions struct {
    Host      string
    Port      int
    Timeout   time.Duration
    MaxConns  int
    TLSConfig *tls.Config
    Logger    *Logger
}

func NewServer(opts ServerOptions) (*Server, error)

// 或：使用变长选项模式（更灵活）
func NewServer(opts ...ServerOption) (*Server, error)
```

### 13.6 魔法数字与字符串

所有字面量中带有业务含义的数字、字符串都应提取为命名常量。

```go
// 坏：魔法数字
if retryCount > 3 {
    return fmt.Errorf("max retries exceeded")
}
time.Sleep(5 * time.Second)

// 好：命名常量
const (
    maxRetries     = 3
    retryDelay     = 5 * time.Second
    defaultTimeout = 30 * time.Second
)

if retryCount > maxRetries {
    return fmt.Errorf("max retries exceeded")
}
time.Sleep(retryDelay)
```

### 13.7 单行职责

一行代码只做一件事。

```go
// 坏：一行多事
result, err := process(validate(clean(input))); if err != nil { return err }

// 好：逐步清晰
input = clean(input)
if err := validate(input); err != nil {
    return err
}
result, err := process(input)
if err != nil {
    return err
}
```

### 13.8 注释比例与质量

| 规范 | 说明 |
|------|------|
| **注释解释 Why，代码解释 What** | 好的代码应该自解释"做什么" |
| **导出符号必须注释** | 所有 `Exported` 名称需要 godoc 注释 |
| **复杂算法必须注释** | 解释算法选择的原因和关键步骤 |
| **TODO/FIXME 要留痕迹** | `// TODO(username): 说明原因和计划修复时间` |
| **避免注释掉的代码** | 直接删除，Git 会保留历史 |

### 13.9 代码质量检查工具

```bash
# 圈复杂度检查
go install github.com/fzipp/gocyclo/cmd/gocyclo@latest
gocyclo -over 15 .

# 重复代码检测
go install github.com/mibk/dupl@latest
dupl -t 200 .

# 综合质量检查（含复杂度、行数等）
golangci-lint run --enable=gocyclo,dupl,funlen

# 函数长度限制（funlen 插件）
# 默认：函数 60 行，语句 40 条
```

---

## 十四、项目目录结构（Standard Go Project Layout）

> 参考 golang-standards/project-layout，适用于中大型项目

### 14.1 推荐目录结构

```
myproject/
├── cmd/                    # 应用程序入口点（每个子目录一个可执行文件）
│   ├── api/
│   │   └── main.go         # API 服务主程序
│   └── worker/
│       └── main.go         # 后台任务主程序
│
├── internal/               # 私有代码，不允许外部项目导入
│   ├── app/                # 应用层逻辑
│   │   ├── api/            # API 服务实现
│   │   └── worker/         # 后台任务实现
│   ├── auth/               # 认证相关
│   ├── storage/            # 数据存储层
│   ├── transport/          # 传输层（HTTP/gRPC）
│   └── pkg/                # 项目内部共享工具（小范围复用）
│
├── pkg/                    # 可被外部项目导入的公共库
│   ├── logger/             # 日志工具
│   ├── crypto/             # 加密工具
│   └── validator/          # 验证工具
│
├── api/                    # API 定义文件（OpenAPI/Protobuf）
│   ├── openapi.yaml
│   └── proto/
│
├── configs/                # 配置文件模板
│   └── config.yaml
│
├── scripts/                # 构建、部署脚本
│   ├── build.sh
│   └── deploy.sh
│
├── docs/                   # 设计文档和用户手册
│
├── test/                   # 外部测试、集成测试数据和工具
│
├── web/                    # Web 应用静态文件（如有前端）
│
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

### 14.2 目录说明

| 目录 | 用途 | 访问权限 |
|------|------|----------|
| `cmd/` | 每个子目录对应一个可执行文件，保持 `main.go` 最小化 | 公开 |
| `internal/` | 项目私有代码，Go 编译器会阻止外部导入 | 私有 |
| `pkg/` | 可被其他项目导入的公共库代码 | 公开 |
| `api/` | API 契约定义（OpenAPI、Protobuf、GraphQL 等） | 公开 |
| `configs/` | 配置文件模板和示例 | 公开 |
| `scripts/` | 构建、分析、部署等辅助脚本 | 公开 |
| `docs/` | 设计文档、架构图、用户手册 | 公开 |
| `test/` | 额外的外部测试和测试数据 | 公开 |
| `web/` | Web 应用静态资源 | 公开 |

### 14.3 关键原则

- **`cmd/` 保持最小化**：`main.go` 只负责依赖注入、配置加载和启动应用，业务逻辑在 `internal/` 中实现
- **`internal/` 优先**：默认将代码放在 `internal/`，只有确实需要被外部导入时才移到 `pkg/`
- **避免 `util`/`common`/`helper` 包**：即使是 `internal/pkg/utils` 也应按功能拆分，如 `internal/logx`、`internal/errx`
- **按功能而非层级组织**：`internal/auth/` 优于 `internal/models/user.go` + `internal/services/auth.go`
- **每个 `cmd/` 子目录一个 `main.go`**：不要在一个目录中放多个入口文件

### 14.4 小型项目简化

对于面试项目或小型工具，可以简化：

```
small-project/
├── main.go                 # 单入口
├── go.mod
├── internal/               # 核心业务逻辑
│   └── service.go
├── pkg/                    # 可复用工具（可选）
│   └── helper.go
└── README.md
```

> 不要为了使用而使用复杂结构，根据项目规模灵活调整。

