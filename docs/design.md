# 系统设计文档：基于 AI 的 Prometheus 无效指标统计与智能识别分析工具

## 一、整体架构

### 1.1 架构分层图

```
┌─────────────────────────────────────────────────────────────────┐
│                          CLI 入口层                              │
│              cobra CLI (analyze / report / compare)             │
└───────────────────────────┬─────────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────────┐
│                         接入层 Ingestion                         │
│    ┌──────────────────────┐    ┌──────────────────────────┐     │
│    │  本地文本文件读取      │    │  Prometheus HTTP API     │     │
│    │  (Prometheus格式)    │    │  /api/v1/label /series   │     │
│    └──────────┬───────────┘    └────────────┬─────────────┘     │
└──────────────────────────────────────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────────┐
│                         解析层 Parser                            │
│         Prometheus Text Format Parser                           │
│         标准化输出：MetricFamily → []Metric                      │
└───────────────────────────┬─────────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────────┐
│                       检测层 Detector                            │
│  ┌────────────────┐ ┌──────────┐ ┌──────────┐                   │
│  │高基数(100×+)   │ │空值指标   │ │违规标签   │ ← 严重(Critical) │
│  │  HighCard.     │ │  Empty   │ │ Illegal  │                   │
│  └────────────────┘ └──────────┘ └──────────┘                   │
│  ┌────────────────┐ ┌──────────┐ ┌──────────┐                   │
│  │高基数(10×~100×)│ │重复指标   │ │孤儿指标   │ ← 一般(Major)   │
│  │  HighCard.     │ │Duplicate │ │  Orphan  │                   │
│  └────────────────┘ └──────────┘ └──────────┘                   │
│  ┌────────────────┐ ┌──────────┐ ┌──────────┐                   │
│  │高基数(1×~10×)  │ │冗余指标   │ │废弃指标   │ ← 轻微(Minor)   │
│  │  HighCard.     │ │Redundant │ │Deprecated│                   │
│  └────────────────┘ └──────────┘ └──────────┘                   │
└───────────────────────────┬─────────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────────┐
│                      统计层 Statistics                           │
│   全量统计 / 分类统计 / Top20高风险 / Top20违规标签              │
└───────────────────────────┬─────────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────────┐
│                        AI 分析层                                 │
│                                                                  │
│   ┌─────────────────────────────────────────────────────┐      │
│   │              LLMProvider Interface                   │      │
│   │  Analyze(ctx, prompt) (AnalysisResult, error)       │      │
│   └──────────────────┬──────────────────────────────────┘      │
│                      │                                           │
│         ┌────────────┴────────────┐                             │
│         ▼                         ▼                             │
│  ┌─────────────┐         ┌──────────────┐                       │
│  │ClaudeProvider│         │DeepSeekProvider│                    │
│  │(claude-3.5) │         │(deepseek-chat) │                    │
│  └─────────────┘         └──────────────┘                       │
│                                                                  │
│   AI输出：根因分析 / 风险评定 / 优化建议 / 治理报告              │
└───────────────────────────┬─────────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────────┐
│                       输出层 Reporter                            │
│    ┌──────────────┐  ┌──────────────┐  ┌──────────────┐        │
│    │  控制台输出   │  │  Markdown    │  │    Excel     │        │
│    │  Console     │  │  报表导出    │  │   报表导出    │        │
│    └──────────────┘  └──────────────┘  └──────────────┘        │
└─────────────────────────────────────────────────────────────────┘
```

### 1.2 模块职责说明

| 层级 | 模块 | 职责 |
|------|------|------|
| CLI层 | `cmd/` | 命令入口，参数解析，流程编排 |
| 接入层 | `internal/ingestion/` | 统一接入接口，屏蔽数据来源差异 |
| 解析层 | `internal/parser/` | 解析 Prometheus 文本格式，结构化输出 |
| 检测层 | `internal/detector/` | 七类无效指标规则检测，三级风险分级 |
| 统计层 | `internal/statistics/` | 聚合计算各维度统计数据 |
| AI层 | `internal/ai/` | LLM抽象接口 + 各Provider实现 + Prompt工程 |
| 输出层 | `internal/reporter/` | 多格式报告生成 |
| 基础设施 | `internal/config/` `pkg/logger/` | 配置管理、日志 |

---

## 二、数据流转流程

```
输入
  │
  ▼
[数据接入] ──── 本地 .txt 文件 / Prometheus HTTP API
  │
  ▼ RawMetricText / MetricSeries[]
[解析器] ──── 解析 Prometheus Exposition Format
  │             name{label1="v1", label2="v2"} value timestamp
  ▼ []Metric
[检测引擎] ──── 并行执行七类检测规则
  │             每条 Metric → []DetectedIssue (type + severity)
  ▼ []InvalidMetric
[统计引擎] ──── 聚合：总量、分类占比、Top20、违规标签榜
  │
  ▼ StatisticsReport
[AI分析器] ──── 构造 Prompt → 调用 LLMProvider
  │             Claude / DeepSeek → 根因 + 建议 + 评级
  ▼ AIAnalysisResult
[报告生成] ──── 组合 StatisticsReport + AIAnalysisResult
  │
  ▼
输出：Console / report.md / report.xlsx / analysis.log
```

---

## 三、目录结构

```
prometheus/
├── cmd/
│   ├── main.go                  # 程序入口
│   └── root.go                  # cobra 根命令 + 子命令注册
├── internal/
│   ├── config/
│   │   └── config.go            # 配置结构体，加载 config.json
│   ├── ingestion/
│   │   ├── ingestion.go         # Ingester 接口定义
│   │   ├── file_reader.go       # 本地文件接入实现
│   │   └── prometheus_client.go # Prometheus HTTP API接入实现
│   ├── parser/
│   │   └── parser.go            # Prometheus Exposition Format 解析
│   ├── detector/
│   │   ├── detector.go          # 检测引擎入口，并行调度规则，透传动态服务集
│   │   └── rules/
│   │       ├── rule.go          # Rule 接口定义
│   │       ├── deprecated.go    # 废弃指标规则
│   │       ├── duplicate.go     # 重复指标规则
│   │       ├── empty.go         # 空名称/空标签/空值规则
│   │       ├── illegal_label.go # 违规字符标签规则
│   │       ├── orphan.go        # 孤儿指标规则
│   │       ├── high_cardinality.go # 高基数指标规则
│   │       └── redundant.go     # 冗余无意义指标规则
│   ├── statistics/
│   │   └── statistics.go        # 统计聚合：总量/分类/Top20
│   ├── ai/
│   │   ├── provider.go          # LLMProvider 接口
│   │   ├── analyzer.go          # AI分析编排，Prompt构造
│   │   ├── claude/
│   │   │   └── claude.go        # Claude API Provider实现
│   │   └── deepseek/
│   │       └── deepseek.go      # DeepSeek API Provider实现
│   └── reporter/
│       ├── reporter.go          # Reporter 接口定义
│       ├── console.go           # 控制台格式化输出
│       ├── markdown.go          # Markdown报表生成
│       └── excel.go             # Excel报表生成
├── pkg/
│   └── logger/
│       └── logger.go            # 结构化日志（基于 zap）
├── data/
│   └── sample_metrics.txt       # 模拟 Prometheus 指标数据
├── config.json                  # 配置文件
├── Dockerfile                   # 容器化打包
├── helm/
│   ├── Chart.yaml
│   ├── values.yaml
│   └── templates/
│       ├── cronjob.yaml             # CronJob 定时触发分析任务
│       ├── configmap.yaml           # config.json 配置注入
│       └── pvc.yaml                 # 报告持久化存储
├── go.mod
└── go.sum
```

---

## 四、关键数据结构设计

```go
// ── 基础结构 ──────────────────────────────────────────────

// Label 标签键值对
type Label struct {
    Key   string
    Value string
}

// Metric 解析后的单条指标
type Metric struct {
    Name      string
    Labels    []Label
    Value     float64
    Timestamp int64   // Unix ms，0 表示无时间戳
    RawLine   string  // 原始文本，用于日志定位
}

// ── 检测结果 ──────────────────────────────────────────────

// InvalidType 无效指标类型枚举
type InvalidType string

const (
    TypeDeprecated     InvalidType = "deprecated"      // 废弃指标
    TypeDuplicate      InvalidType = "duplicate"       // 重复指标
    TypeEmpty          InvalidType = "empty"           // 空名称/标签/值
    TypeIllegalLabel   InvalidType = "illegal_label"   // 违规字符标签
    TypeOrphan         InvalidType = "orphan"          // 孤儿指标
    TypeHighCardinality InvalidType = "high_cardinality" // 高基数
    TypeRedundant      InvalidType = "redundant"       // 冗余无意义
)

// Severity 风险等级
type Severity string

const (
    SeverityCritical Severity = "critical" // 严重无效
    SeverityMajor    Severity = "major"    // 一般无效
    SeverityMinor    Severity = "minor"    // 轻微无效
)

// DetectedIssue 单条检测问题
type DetectedIssue struct {
    Type        InvalidType
    Severity    Severity
    Description string
}

// InvalidMetric 无效指标完整记录
type InvalidMetric struct {
    Metric   Metric
    Issues   []DetectedIssue
    MaxSeverity Severity // 最高风险级别，用于排序
}

// ── 统计结果 ──────────────────────────────────────────────

// TypeStats 单类无效指标统计
type TypeStats struct {
    Count   int
    Percent float64
}

// StatisticsReport 统计报告
type StatisticsReport struct {
    TotalMetrics    int
    ValidMetrics    int
    InvalidMetrics  int
    InvalidPercent  float64

    ByType map[InvalidType]TypeStats

    BySeverity map[Severity]int

    // Top20 高风险无效指标（按 MaxSeverity + Issues数量排序）
    Top20Invalid []InvalidMetric

    // Top20 违规标签 Key（出现次数排序）
    Top20IllegalLabels []LabelStat
}

// LabelStat 标签统计
type LabelStat struct {
    LabelKey string
    Count    int
    Examples []string // 举例说明
}

// ── AI 分析结果 ───────────────────────────────────────────

// RootCause 根因分析
type RootCause struct {
    Category    string // 如：服务下线未清理、标签滥用、配置错误
    Description string
    AffectedMetrics []string
}

// Suggestion 优化建议
type Suggestion struct {
    Priority    int    // 1=高优先级
    Action      string // 如：清理、合并、整改
    Detail      string
    RelabelRule string // 可选：生成的 relabel 规则片段
}

// AIAnalysisResult AI分析结果
type AIAnalysisResult struct {
    OverallScore    int    // 监控治理健康评分 0-100
    RiskSummary     string // 整体风险摘要
    RootCauses      []RootCause
    Suggestions     []Suggestion
    GovernanceReport string // 完整治理报告（Markdown）
    Provider        string  // 使用的 LLM Provider
    Model           string  // 使用的具体模型
}

// ── LLM Provider 接口 ────────────────────────────────────

// LLMProvider AI分析提供者接口
type LLMProvider interface {
    // Name 返回 Provider 名称
    Name() string
    // Analyze 发送分析请求，返回原始响应文本
    Analyze(ctx context.Context, prompt string) (string, error)
}

// ── 配置结构 ──────────────────────────────────────────────

// Config 全局配置（对应 config.json）
type Config struct {
    // 数据接入
    Ingestion IngestionConfig `json:"ingestion"`

    // AI 配置
    AI AIConfig `json:"ai"`

    // 检测规则阈值
    Detection DetectionConfig `json:"detection"`

    // 报告输出
    Report ReportConfig `json:"report"`

    // 日志
    Log LogConfig `json:"log"`
}

type IngestionConfig struct {
    Mode           string `json:"mode"`            // "file" | "prometheus"
    FilePath       string `json:"file_path"`       // mode=file 时使用
    PrometheusURL  string `json:"prometheus_url"`  // mode=prometheus 时使用
}

type AIConfig struct {
    Provider string `json:"provider"` // "claude" | "deepseek"
    APIKey   string `json:"api_key"`  // 运行时从环境变量覆盖
    Model    string `json:"model"`    // 模型名称
    Timeout  int    `json:"timeout"`  // 请求超时秒数
    Enabled  bool   `json:"enabled"`  // 是否启用 AI 分析
}

type DetectionConfig struct {
    DeprecatedPrefixes      []string `json:"deprecated_prefixes"`      // 废弃前缀列表
    DeprecatedSuffixes      []string `json:"deprecated_suffixes"`      // 废弃后缀列表
    KnownServiceNames       []string `json:"known_service_names"`      // 已知服务名
    HighCardinalityThreshold int     `json:"high_cardinality_threshold"` // 高基数阈值
    RedundantPatterns       []string `json:"redundant_patterns"`       // 冗余指标正则
}

type ReportConfig struct {
    OutputDir      string `json:"output_dir"`      // 报告输出目录
    EnableMarkdown bool   `json:"enable_markdown"` // 是否输出 Markdown
    EnableExcel    bool   `json:"enable_excel"`    // 是否输出 Excel
}

type LogConfig struct {
    Level  string `json:"level"`  // debug | info | warn | error
    File   string `json:"file"`   // 日志文件路径，空则输出到 stdout
}
```

---

## 五、无效指标检测算法思路与分类规则

> 各类型的完整检测链路、置信度体系、输出示例及已知局限性，详见 **[detection-rules.md](./detection-rules.md)**。

### 5.1 七类检测规则总览

工具将无效指标分为两大类：**确定性问题**（有明确的数据模型或行为证据）和**候选问题**（基于多维信号推断，含置信度标注，需人工复核）。

| 类型 | 类别 | 检测方式 | 风险等级 |
|------|------|---------|----------|
| **高基数指标** | 确定性 | Label 组合数 ÷ 阈值，按倍数分级 | 按倍数：1×~10× 轻微 / 10×~100× 一般 / 100×+ 严重 |
| **空值/NaN 指标** | 确定性 | 空名称 / 空 Label Key / 全空 Value / NaN 值 | 严重 |
| **违规标签指标** | 确定性 | Label Key 不符合规范 / 保留前缀 / 超长 | 严重 |
| **重复指标** | 确定性 | 相同名称 + 完整 Label 键值对重复出现 | 一般 |
| **孤儿指标** | 确定性 | 动态：服务标签不在 `/api/v1/targets` 存活列表；静态：前缀不在白名单 | 一般 |
| **疑似废弃指标** | 候选 | 两阶段多信号：HELP 文本 / 生命周期标签 / 规则引用 / 名称启发 | 轻微 |
| **疑似冗余指标** | 候选 | 两阶段多信号：环境标签 / 历史行为 / 规则引用 / 名称启发 / 结构特征 | 轻微 |

### 5.2 风险三级分级策略

分级的核心维度是**对 Prometheus 整体稳定性和成本的威胁程度**。

```
严重（Critical）：高基数（100×+） | 空值/NaN | 违规标签
  → 直接威胁 Prometheus 进程稳定性或造成整个 target 数据缺失，立即处理

一般（Major）：高基数（10×~100×） | 重复指标 | 孤儿指标
  → 影响数据可信度或持续消耗资源，纳入近期治理计划

轻微（Minor）：高基数（1×~10×） | 疑似冗余 | 疑似废弃
  → 治理技术债务，不立即影响稳定性，结合置信度定期清理
```

一条指标可同时命中多个规则，取最高风险等级作为 `MaxSeverity`。

### 5.3 候选类型置信度体系

疑似废弃和疑似冗余两类采用**两阶段全量收集 + 综合定级**模式，不短路返回。

| 置信度 | 含义 | 控制台展示 | 建议行动 |
|--------|------|-----------|---------|
| `HIGH` | 有明确证据（HELP 声明 / 历史行为） | 无前缀 | 可直接纳入清理计划 |
| `MEDIUM` | 有间接证据（环境标签 / 规则未引用） | `[置信度:中]` | 与 owner 确认后处理 |
| `LOW` | 仅名称/结构启发式信号 | `[置信度:低]` | 人工核实后再操作 |

### 5.4 Prometheus 模式动态增强

在 `ingestion.mode = "prometheus"` 时，工具自动调用三组额外 API 提升检测质量：

| API | 用途 | 使用的规则 |
|-----|------|-----------|
| `/api/v1/targets?state=active` | 获取存活服务集合 | 孤儿规则动态模式 |
| `/api/v1/rules` | 获取规则引用表 | 废弃规则 Signal D + 冗余规则 Signal D |
| `/api/v1/query`（批量，每批 20 条） | 历史行为查询（stale / always-zero / static） | 冗余规则 Signal E |

任意 API 失败均 warn 日志并自动降级，不阻断分析流程。

---

## 六、AI 模块设计

### 6.1 LLM Provider 接口设计

```
              ┌──────────────────┐
              │   LLMProvider    │  ← 接口
              │  Name() string   │
              │  Analyze(ctx,    │
              │   prompt) string │
              └────────┬─────────┘
                       │
           ┌───────────┴────────────┐
           ▼                        ▼
  ┌────────────────┐      ┌──────────────────┐
  │ ClaudeProvider │      │ DeepSeekProvider  │
  │ claude-3.5/4.x │      │ deepseek-chat     │
  │ Anthropic SDK  │      │ OpenAI Compatible │
  └────────────────┘      └──────────────────┘
```

通过 `config.json` 中的 `ai.provider` 字段在启动时选择 Provider，运行时无需修改代码。

### 6.2 Claude 选型理由

- **结构化输出能力强**：可精确按 Prompt 要求返回 JSON / Markdown 格式报告
- **长上下文支持**：单次可分析大批量指标摘要（200K token 上下文）
- **技术推理准确**：对 Prometheus 监控领域的根因分析和建议质量高
- **指令遵循精准**：多步骤 Prompt（根因→建议→评分）执行稳定

### 6.3 Prompt 工程设计

AI 分析分两阶段调用，避免单次 Prompt 过长：

**阶段一：根因分析 Prompt**
```
输入：统计报告摘要（总量/各类无效数/Top10问题指标）
任务：
  1. 识别主要无效指标模式
  2. 分析每种模式的可能根因（服务下线/标签滥用/配置错误等）
  3. 输出 JSON 格式根因列表
```

**阶段二：治理建议与评分 Prompt**
```
输入：阶段一根因 + 统计报告
任务：
  1. 针对每个根因给出 3 条可落地建议
  2. 提供 Prometheus relabel 规则示例
  3. 给出整体监控健康评分（0-100）及说明
  4. 生成 Markdown 格式完整治理报告
```

---

## 七、配置文件设计（config.json）

```json
{
  "ingestion": {
    "mode": "file",
    "file_path": "./data/sample_metrics.txt",
    "prometheus_url": "http://localhost:9090"
  },
  "ai": {
    "provider": "claude",
    "api_key": "",
    "model": "claude-sonnet-4-6",
    "timeout": 60,
    "enabled": true
  },
  "detection": {
    "deprecated_prefixes": ["old_", "legacy_", "deprecated_"],
    "deprecated_suffixes": ["_old", "_legacy", "_deprecated"],
    "known_service_names": ["payment", "order", "user", "inventory", "gateway", "auth"],
    "high_cardinality_threshold": 1000,
    "redundant_patterns": ["^test_", "^debug_", "^tmp_", ".*_\\d+$"],
    "service_label_keys": ["job", "app", "service", "app_kubernetes_io_name", "service_name", "k8s_app"]
  },
  "report": {
    "output_dir": "./output",
    "enable_markdown": true,
    "enable_excel": true
  },
  "log": {
    "level": "info",
    "file": "./output/analysis.log"
  }
}
```

---

## 八、命令行接口设计（CLI）

```bash
# 完整分析流程（默认命令）
prometheus-analyzer analyze \
  --config config.json \
  --input ./data/sample_metrics.txt \
  --provider claude \
  --api-key $CLAUDE_API_KEY

# 仅统计，不调用 AI
prometheus-analyzer analyze --no-ai

# 指定输出格式
prometheus-analyzer analyze --output markdown,excel,console

# 单独分析某个指标（加分项：交互模式）
prometheus-analyzer inspect --metric "http_request_total" --label "env=test"

# 历史对比（加分项）
prometheus-analyzer compare \
  --baseline ./output/scan_20240101.json \
  --current  ./output/scan_20240102.json
```

---

## 九、容器化与 K8s 部署方案

### 9.1 Dockerfile

```dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o prometheus-analyzer ./cmd

FROM alpine:3.19
WORKDIR /app
COPY --from=builder /app/prometheus-analyzer .
COPY config.json .
COPY data/ ./data/
RUN mkdir -p /app/output
ENTRYPOINT ["./prometheus-analyzer"]
```

### 9.2 Helm Chart 文件结构

| 文件 | 资源类型 | 说明 |
|------|---------|------|
| `templates/cronjob.yaml` | `batch/v1 CronJob` | 定时触发分析任务，挂载 ConfigMap 和 PVC，Secret 注入 API Key |
| `templates/configmap.yaml` | `v1 ConfigMap` | 将 `values.yaml` 中的配置渲染为 `config.json`，挂载到容器 `/app/config/` |
| `templates/pvc.yaml` | `v1 PersistentVolumeClaim` | 持久化存储分析报告，`persistence.enabled=false` 时不创建 |

### 9.3 核心 values 配置

```yaml
image:
  repository: prometheus-analyzer
  tag: latest
  pullPolicy: IfNotPresent

schedule: "0 2 * * *"      # 每天凌晨 2 点执行

secret:
  name: ai-api-key          # K8s Secret 名称，API Key 由此注入（不写入 values.yaml）
  claudeKeyField: CLAUDE_API_KEY
  deepseekKeyField: DEEPSEEK_API_KEY

persistence:
  enabled: true
  size: 1Gi
  mountPath: /app/output

config:
  ingestion:
    mode: prometheus
    prometheusUrl: "http://prometheus.monitoring.svc.cluster.local:9090"
  ai:
    provider: claude
    model: claude-sonnet-4-6
    timeout: 60
    enabled: true
  detection:
    highCardinalityThreshold: 1000
```

部署方式：以 **CronJob** 形式定时触发分析（默认每日凌晨 2 点），输出的 Markdown / Excel 报告写入 PVC，`config.json` 通过 ConfigMap 渲染注入，API Key 通过 K8s Secret 环境变量注入，不落盘。

---

## 十、加分项实现规划

| 优先级 | 功能 | 实现方案 |
|--------|------|----------|
| P1 | **命令行交互模式** | `inspect` 子命令，支持 `--metric` `--label` 参数单独分析 |
| P2 | **自动生成 Relabel 规则** | 基于检测结果，按模板生成 `metric_relabel_configs` YAML 片段，附在报告末尾 |
| P3 | **历史对比分析** | 每次扫描结果序列化为带时间戳的 JSON 文件，`compare` 子命令加载两份对比差量 |
| P4 | **模拟 TSDB 索引分析** | 构建内存倒排索引，估算各指标 Series 数与存储字节占用，输出"存储膨胀 Top10" |
| P5 | **CI/CD 准入拦截** | `validate` 子命令，检测指标文件，严重问题返回非零 exit code，集成 GitHub Actions |
