# Prometheus 无效指标智能分析工具

基于 AI 大模型的 Prometheus 无效指标统计与智能识别分析工具，实现**指标采集 → 无效指标检测 → AI 根因分析 → 优化建议 → 报告输出**全链路能力。

---

## 功能特性

### 七类无效指标检测

分级依据是**对 Prometheus 整体稳定性和成本的威胁程度**。完整检测规则详见 [docs/detection-rules.md](docs/detection-rules.md)。

**确定性检测**（有明确证据，无需人工复核）

| 类型 | 说明 | 风险等级 |
|------|------|----------|
| 高基数指标 | Label 组合数超阈值，按超出倍数分级：100×+ OOM 风险 / 10×~100× 显著 / 1×~10× 轻微 | 🔴🟡🟢 按倍数 |
| 空值/NaN 指标 | 空名称、空 Label Key、全空 Value、NaN 值 | 🔴 严重 |
| 违规标签指标 | Label Key 含非法字符、保留前缀 `__`、超长 | 🔴 严重 |
| 重复指标 | 完全相同的名称+标签键值对重复上报 | 🟡 一般 |
| 孤儿指标 | 动态模式：服务标签不在存活 target 中；静态模式：前缀不在白名单 | 🟡 一般 |

**候选检测**（多信号推断，附置信度标注，建议人工复核）

| 类型 | 强证据（HIGH）| 中等证据（MEDIUM）| 弱信号（LOW）|
|------|-------------|-----------------|------------|
| 疑似废弃 | HELP 文本含 `DEPRECATED` / 生命周期标签 | 未被 Prometheus 规则引用 | 名称前缀/后缀/关键字 |
| 疑似冗余 | 历史行为：stale / 7天恒零 / 7天静止 | 环境标签（env=dev/test）/ 未被规则引用 | 名称模式 / 无任何标签 |

### AI 智能分析

- 支持 **Claude**（Anthropic）和 **DeepSeek** 两种 LLM Provider，通过配置切换
- 两阶段分析：根因识别 → 治理建议 + 健康评分
- 输出可落地的 Prometheus `metric_relabel_configs` 规则建议

### 多格式报告输出

- 控制台彩色结构化输出
- Markdown 报表（`output/report_*.md`）
- Excel 报表（`output/report_*.xlsx`）
- 结构化日志文件（`output/analysis.log`）

---

## 项目结构

```
prometheus/
├── main.go                          # 程序入口
├── config.json                      # 配置文件
├── data/
│   └── sample_metrics.txt           # 模拟 Prometheus 指标数据
├── cmd/
│   ├── root.go                      # cobra 根命令
│   ├── analyze.go                   # analyze 子命令（完整分析流程）
│   └── inspect.go                   # inspect 子命令（单指标交互分析）
├── internal/
│   ├── config/                      # 配置加载
│   ├── model/                       # 核心数据结构
│   ├── parser/                      # Prometheus 文本格式解析
│   ├── ingestion/                   # 数据接入（文件 / Prometheus API）
│   ├── detector/                    # 检测引擎 + 七类规则
│   │   └── rules/
│   ├── statistics/                  # 统计聚合
│   ├── ai/                          # AI 分析层
│   │   ├── provider.go              # LLMProvider 接口
│   │   ├── analyzer.go              # 两阶段 Prompt 编排
│   │   ├── claude/                  # Claude Provider 实现
│   │   └── deepseek/                # DeepSeek Provider 实现
│   └── reporter/                    # 报告输出（Console / Markdown / Excel）
├── pkg/logger/                      # 结构化日志（zap）
├── Dockerfile
└── helm/                            # Helm Chart
    ├── Chart.yaml
    ├── values.yaml
    └── templates/
        ├── cronjob.yaml             # CronJob 定时触发
        ├── configmap.yaml           # config.json 注入
        └── pvc.yaml                 # 报告持久化存储
```

---

## 快速开始

### 环境要求

- Go 1.22+
- （可选）Claude API Key 或 DeepSeek API Key

### 1. 克隆与编译

```bash
git clone https://github.com/memo/prometheus-analyzer.git
cd prometheus-analyzer

# 安装依赖
go mod download

# 编译
go build -o prometheus-analyzer .
```

### 2. 配置文件

编辑 `config.json`，主要配置项：

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
  }
}
```

> `api_key` 建议留空，通过环境变量注入，避免密钥泄露。

### 3. 运行分析

**方式一：仅统计（不调用 AI，无需 API Key）**

```bash
./prometheus-analyzer analyze --no-ai
```

**方式二：完整分析（含 AI 根因分析）**

```bash
# Claude
export CLAUDE_API_KEY=sk-ant-xxxx
./prometheus-analyzer analyze

# DeepSeek
export DEEPSEEK_API_KEY=sk-xxxx
./prometheus-analyzer analyze --provider deepseek
```

**方式三：指定输入文件**

```bash
./prometheus-analyzer analyze --input /path/to/metrics.txt --no-ai
```

**方式四：对接真实 Prometheus**

修改 `config.json` 中 `ingestion.mode` 为 `prometheus`，并设置 `prometheus_url`：

```bash
./prometheus-analyzer analyze
```

---

## 命令参考

### `analyze` — 完整分析流程

```
./prometheus-analyzer analyze [flags]

Flags:
  --config string     配置文件路径 (default "config.json")
  --input string      覆盖输入文件路径
  --provider string   AI provider: claude | deepseek
  --api-key string    API Key（优先级低于环境变量）
  --output string     输出格式，逗号分隔: console,markdown,excel (default "console,markdown,excel")
  --no-ai             跳过 AI 分析，仅输出统计结果
```

### `inspect` — 单指标交互分析（加分项）

```
./prometheus-analyzer inspect [flags]

Flags:
  --metric string     指标名称（支持模糊匹配）
  --label string      标签过滤，格式: key=value

示例：
  ./prometheus-analyzer inspect --metric "deprecated"
  ./prometheus-analyzer inspect --metric "payment" --label "env=prod"
```

---

## API Key 配置说明

支持三种注入方式，优先级从高到低：

| 优先级 | 方式 | 说明 |
|--------|------|------|
| 1 | 环境变量 | `CLAUDE_API_KEY` 或 `DEEPSEEK_API_KEY` |
| 2 | 文件挂载 | `config.json` 中 `api_key_file` 指定路径（Docker Secret 场景） |
| 3 | 配置文件 | `config.json` 中 `api_key` 字段（不推荐提交到代码仓库） |

---

## Docker 部署

### 构建镜像

```bash
docker build -t prometheus-analyzer:latest .
```

### 运行容器

```bash
# 仅统计
docker run prometheus-analyzer --no-ai

# 带 AI 分析（通过环境变量注入 Key）
docker run \
  -e CLAUDE_API_KEY=sk-ant-xxxx \
  -v $(pwd)/output:/app/output \
  prometheus-analyzer

# 对接真实 Prometheus
docker run \
  -e CLAUDE_API_KEY=sk-ant-xxxx \
  -v $(pwd)/config.json:/app/config.json \
  -v $(pwd)/output:/app/output \
  prometheus-analyzer analyze
```

### Docker Compose

```yaml
services:
  analyzer:
    image: prometheus-analyzer:latest
    environment:
      - CLAUDE_API_KEY=${CLAUDE_API_KEY}
    volumes:
      - ./output:/app/output
      - ./config.json:/app/config.json
```

```bash
CLAUDE_API_KEY=sk-ant-xxxx docker compose up
```

---

## Kubernetes 部署（Helm）

以 CronJob 形式定时运行，每日自动分析 Prometheus 指标。

### 1. 创建 Secret

```bash
kubectl create secret generic ai-api-key \
  --from-literal=CLAUDE_API_KEY=sk-ant-xxxx \
  -n monitoring
```

### 2. 安装 Chart

```bash
helm install prometheus-analyzer ./helm \
  --namespace monitoring \
  --set config.ingestion.prometheusUrl=http://prometheus.monitoring.svc:9090 \
  --set config.ai.provider=claude
```

### 3. 手动触发一次分析

```bash
kubectl create job --from=cronjob/prometheus-analyzer manual-run -n monitoring
```

---

## 输出示例

### 控制台输出

```
========================================
  Prometheus 无效指标智能分析报告
========================================

【总览】
  指标总数:            147
  有效指标:             15
  无效指标:            132 (89.8%)

【无效指标分类统计】
  废弃指标:          ░░░░░░░░░░░░░░░░░░░░   5条 (3.4%)
  重复指标:          ░░░░░░░░░░░░░░░░░░░░   6条 (4.1%)
  高基数指标:         █████████████░░░░░░░ 101条 (68.7%)
  孤儿指标:          ██░░░░░░░░░░░░░░░░░░  17条 (11.6%)
  ...

【风险等级分布】
  严重 (Critical):  14 条
  一般 (Major):    118 条
  轻微 (Minor):      0 条
```

### 报告文件

```
output/
├── report_20240605_020000.md    # Markdown 报表
├── report_20240605_020000.xlsx  # Excel 报表（含多 Sheet）
└── analysis.log                 # 结构化日志
```

---

## 技术栈

| 组件 | 技术选型 |
|------|----------|
| 开发语言 | Go 1.22 |
| CLI 框架 | [cobra](https://github.com/spf13/cobra) |
| 日志 | [zap](https://github.com/uber-go/zap) |
| Excel 输出 | [excelize](https://github.com/xuri/excelize) |
| Claude SDK | [anthropic-sdk-go](https://github.com/anthropics/anthropic-sdk-go) |
| 容器化 | Docker 多阶段构建 |
| K8s 部署 | Helm Chart（CronJob 模式） |

---

## 设计文档

| 文档 | 内容 |
|------|------|
| [docs/design.md](docs/design.md) | 整体架构、模块分层、数据流、AI 模块、配置设计、Helm 部署方案 |
| [docs/detection-rules.md](docs/detection-rules.md) | 七类无效指标的完整检测链路、证据体系、置信度综合规则、输出示例、已知局限性 |
