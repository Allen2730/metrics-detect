# 无效指标检测规则详解

> 本文档描述每类无效指标的检测逻辑、证据链路、置信度综合规则及已知局限性。
> 系统架构与模块设计参见 [design.md](./design.md)。

---

## 一、检测框架概述

### 1.1 执行模型

七类规则在检测引擎中**并行执行**，同一条指标可命中多个规则，所有命中结果合并后取最高 `MaxSeverity` 作为该指标的整体风险等级。

```
[]Metric
   │
   ├─ HighCardinalityRule ──┐
   ├─ EmptyRule             │
   ├─ IllegalLabelRule      ├──► 合并 Issues → []InvalidMetric
   ├─ DuplicateRule         │
   ├─ OrphanRule            │
   ├─ CandidateDeprecatedRule│
   └─ CandidateRedundantRule┘
```

### 1.2 双接入模式

| 模式 | 触发条件 | 额外调用的 Prometheus API |
|------|---------|--------------------------|
| **文件模式** | `ingestion.mode = "file"` | 无 |
| **Prometheus 模式** | `ingestion.mode = "prometheus"` | `/api/v1/targets`、`/api/v1/rules`、`/api/v1/query` |

Prometheus 模式下任意 API 调用失败均 warn 日志并降级，**不阻断**主流程。

### 1.3 证据置信度体系

仅「疑似废弃」和「疑似冗余」两类候选类型使用置信度体系，其余五类均为确定性检测。

| 置信度 | 含义 | 控制台展示 |
|--------|------|-----------|
| `HIGH` | 有明确证据，结论可信 | 无前缀标注 |
| `MEDIUM` | 有间接证据，建议确认 | `[置信度:中]` |
| `LOW` | 仅启发式信号，需人工复核 | `[置信度:低]` |

### 1.4 综合定级原则（候选类型通用）

检测时**全量收集所有命中信号**，不短路返回，最终按以下规则综合确定整体置信度：

```
任意 HIGH 信号命中                        → 整体置信度 HIGH
MEDIUM（直接）单独命中                    → 整体置信度 MEDIUM
MEDIUM（补强 booster）+ 任意 LOW 叠加    → 整体置信度 MEDIUM
≥2 个独立 LOW 信号                       → 整体置信度 MEDIUM
仅 1 个 LOW 信号                         → 整体置信度 LOW（建议人工复核）
```

---

## 二、确定性检测类型

> 这五类问题有明确的数据模型或行为证据，检测结果无需人工复核。

---

### 2.1 高基数指标（HighCardinality）

**定义**：同一指标名下，唯一 Label 值组合数（Series 数）超过配置阈值。

**为什么危险**：每条 Series 在 Prometheus Head Block 中独立占用内存，高基数是 Prometheus OOM 最常见根因，同时拖慢 compaction、remote_write 和全局查询。

#### 检测逻辑

```
对全量 Metric 按指标名分组，
统计每组内唯一 fingerprintValues（sorted label key=value 拼接串）数量。

count > threshold → 标记，severity 按超出倍数分级：
  ratio = count / threshold（整除）
  ratio < 10   → Minor   "轻微超标，建议持续观察"
  ratio < 100  → Major   "显著超标，需纳入整改计划"
  ratio ≥ 100  → Critical "极高基数，存在 OOM 风险"
```

**输出示例**
```
[critical] payment_user_transaction
  ↳ Label 组合数为 500000（阈值 1000，超出 500.0 倍），极高基数，存在 OOM 风险
```

**配置项**：`detection.high_cardinality_threshold`（默认 1000）

**局限性**：基于单次快照，无法判断基数是否在增长中。

---

### 2.2 空值/NaN 指标（Empty）

**定义**：指标在数据模型层面存在缺失或非法值，无法被正常查询或参与计算。

#### 检测逻辑

逐条检查以下条件，任意命中均标记为 **Critical**：

| 条件 | 说明 |
|------|------|
| `Name == ""` | 指标名为空，PromQL 无法引用 |
| 存在 `Label.Key == ""` | 空 Label Key 违反数据模型 |
| 所有 Label Value 均为 `""` | 标签维度信息完全缺失 |
| `Value == NaN` | 任何数学运算结果传播为 NaN，使 Dashboard/告警失效 |

**输出示例**
```
[critical] gateway_response_time_ms
  ↳ 指标值为 NaN
[critical] payment_error_count
  ↳ 所有 Label Value 均为空字符串
```

**局限性**：「部分 Label Value 为空」不触发，只检测所有 Value 全为空的情况。

---

### 2.3 违规标签指标（IllegalLabel）

**定义**：Label Key 不符合 Prometheus 规范，可能导致 scrape 失败或查询异常。

#### 检测逻辑

对每个 Label Key 逐一检查，任意命中均标记为 **Critical**：

| 条件 | 风险说明 |
|------|---------|
| Key 不符合 `[a-zA-Z_][a-zA-Z0-9_]*` | 包含连字符、数字开头、特殊字符等，部分版本会拒绝整批 scrape 数据 |
| Key 以 `__` 开头（非系统指标） | 保留前缀，可能与 Prometheus 内部标签冲突 |
| Key 长度 > 64 字符 | 超出部分后端系统的 Label Key 长度限制 |

**输出示例**
```
[critical] product_view_total
  ↳ Label Key 'product-id' 包含非法字符，不符合 [a-zA-Z_][a-zA-Z0-9_]* 规范
[critical] cart_item_count
  ↳ Label Key '__internal_flag' 使用了保留前缀 '__'
```

**局限性**：不检测 Label Value 的合规性（Prometheus 对 Value 几乎无限制）。

---

### 2.4 重复指标（Duplicate）

**定义**：完全相同的指标名 + 全部 Label 键值对组合在同一批采集数据中出现多次。

**注意**：同名指标携带不同 Label Value（如 `status="200"` 与 `status="500"`）是正常多时序，**不触发**此规则。

#### 检测逻辑

```
fingerprint = metricName + "{" + sorted(key=value pairs) + "}"

按 fingerprint 统计出现次数：
  count > 1 → 标记为 Major
  描述中注明重复上报次数
```

**输出示例**
```
[major] order_service_requests_total
  ↳ 完全相同的指标（名称+标签完全一致）重复上报 2 次
```

**影响**：`sum()`/`rate()` 等聚合计算结果翻倍，告警阈值因此失效。

**局限性**：基于单批数据内的重复，无法跨批次检测（如同一指标由两个不同 exporter 上报）。

---

### 2.5 孤儿指标（Orphan）

**定义**：指标所属服务已不存在于当前运行的服务清单中，为无主指标。

孤儿规则根据接入方式自动切换检测策略，两种模式均标记为 **Major**。

#### 动态模式（Prometheus 模式）

```
调用 /api/v1/targets?state=active
仅收录 health=up 的 target

对每条 Metric：
  按 service_label_keys 优先级从 Labels 中提取服务名：
    job → app → service → app_kubernetes_io_name → service_name → k8s_app
  
  ├── 找到服务名 且 在存活列表中   → 有效，不标记
  ├── 找到服务名 但 不在存活列表  → 孤儿（高置信）
  └── 找不到任何服务标签         → 孤儿（无法归属）
```

#### 静态模式（文件模式 / 动态降级）

```
对每条 Metric：
  优先从 Labels 按 service_label_keys 提取服务名
    → 服务名不在 known_service_names 白名单 → 孤儿（低置信，白名单可能不完整）
  
  无服务标签时退回名称前缀推断
    → 取指标名首个 _ 前的段（如 payment_request_total → payment）
    → 不在 known_service_names → 孤儿（弱信号，需结合配置准确性判断）
```

**输出示例**
```
[major] billing_invoice_total
  ↳ 标签 job="billing-svc" 对应的服务不在 Prometheus 存活 target 中，疑为孤儿指标

[major] analytics_event_track
  ↳ 指标名前缀 "analytics" 不在已知服务列表中，疑为孤儿指标（无服务标签，以名称前缀推断）
```

**配置项**：`detection.known_service_names`、`detection.service_label_keys`

**局限性**：
- 文件模式依赖白名单人工维护，新服务上线需手动更新
- 动态模式有 `/api/v1/targets` 查询延迟，服务重启瞬间可能误报
- 无标签的指标只能靠名称前缀推断，准确率有限

---

## 三、候选检测类型

> 这两类问题基于多维信号推断，存在一定误报可能，结果附有置信度标注，建议人工复核后再处置。

---

### 3.1 疑似废弃指标（CandidateDeprecated）

**定义**：指标可能已不再被维护或推荐使用，但缺乏足够证据排除误报。

**与"确认废弃"的区别**：本工具只能判定"疑似"，真正的废弃声明应在 HELP 文本或生命周期标签中显式体现。仅凭名称中含 `old_`、`legacy_` 等词不足以确认废弃。

#### 检测链路（全量收集 + 综合定级）

```
Phase 1：静态分析（文件 + Prometheus 模式均执行）

  Signal A：HELP 文本显式声明                           → 置信度 HIGH
    关键词：deprecated / obsolete / do not use /
            will be removed / replaced by
    示例 HELP：
      "DEPRECATED: use payment_request_total instead."
      "This metric is deprecated, replaced by order_v2_total"

  Signal B：生命周期标签                                → 置信度 HIGH
    deprecated=true / deprecated=1
    sunset_date=<date>
    replaced_by=<new_metric_name>

  Signal C：名称启发式（弱信号）                         → 置信度 LOW
    前缀：old_ / legacy_ / deprecated_
    后缀：_old / _legacy / _deprecated
    名称内含关键字：deprecated / obsolete / removed / unused

Phase 2：动态分析（仅 Prometheus 模式，RuleReferences 非 nil 时执行）

  Signal D：规则引用分析（复用 /api/v1/rules 结果）
    未被任何告警规则引用 → 补强信号（MEDIUM booster）
    未被任何记录规则引用 → 补强信号（MEDIUM booster）
    仍被规则引用         → 反证输出，提示"存在活跃消费者"

Phase 3：综合定级

  有任意 HIGH 信号                          → 整体置信度 HIGH
  MEDIUM booster + 任意 LOW 叠加           → 整体置信度 MEDIUM
  ≥2 个独立 LOW 信号                       → 整体置信度 MEDIUM
  仅 1 个 LOW 信号                         → 整体置信度 LOW
  无任何静态信号（仅 Signal D）             → 不标记（Signal D 不能单独触发）
```

**置信度对应行动建议**

| 置信度 | 建议 |
|--------|------|
| HIGH | 可直接纳入清理计划，确认替代指标后删除 |
| MEDIUM | 与指标 owner 确认后再清理 |
| LOW | 仅作观察，需人工核实后才能操作 |

**输出示例**
```
# HIGH 置信度（HELP 显式声明）
[minor] old_payment_gateway_requests
  ↳ HELP 文本显式声明废弃: "DEPRECATED: use payment_request_total instead."
  ↳ [置信度:低] 名称前缀 "old_" 疑似废弃（弱信号，仅名称启发）

# MEDIUM 置信度（名称启发 + 未被规则引用）
[minor] legacy_batch_processor
  ↳ [置信度:中] 名称前缀 "legacy_" 疑似废弃（弱信号，仅名称启发）
               + 未被任何 Prometheus 规则引用

# LOW 置信度（仅名称启发）
[minor] legacy_user_session_count
  ↳ [置信度:低] 名称前缀 "legacy_" 疑似废弃（弱信号，仅名称启发，需人工复核）
```

**配置项**：`detection.deprecated_prefixes`、`detection.deprecated_suffixes`

**已知局限性**

| 局限性 | 说明 |
|--------|------|
| Grafana 消费盲区 | Signal D 仅检测 Prometheus 原生规则，Grafana Dashboard 引用无法覆盖 |
| 历史查询证据缺失 | 是否有"值正在下降/逐渐停止上报"的行为证据尚未实现 |
| 替换关系无法自动验证 | `replaced_by` 标签指向的新指标是否真实存在未做二次确认 |
| 误报来源 | `legacy_system`（历史兼容层仍在使用）等命名可能误报 |

---

### 3.2 疑似冗余指标（CandidateRedundant）

**定义**：指标可能对当前监控体系无实质贡献，包括：无人消费、无信息量（长期为零/恒定）、非生产临时指标等。

**与"确认冗余"的区别**：消费证据有 Grafana 盲区，行为证据基于历史快照，均不能独立确认冗余。最终决策需结合业务上下文。

#### 检测链路（全量收集 + 综合定级）

```
Phase 1：静态分析（文件 + Prometheus 模式均执行）

  Signal A：环境/用途标签                               → 置信度 MEDIUM（直接）
    env / environment 值为：
      test / testing / dev / development /
      debug / local / ci / sandbox
    可单独达到 MEDIUM 置信度

  Signal B：名称启发式（弱信号）                        → 置信度 LOW
    前缀匹配：test_ / debug_ / tmp_ / mock_ / fake_
    （由 detection.redundant_patterns 配置）

  Signal C：结构特征（弱信号）                          → 置信度 LOW
    指标完全无 Label（无法按任何维度聚合/过滤）

Phase 2：动态分析（仅 Prometheus 模式，对应字段非 nil 时执行）

  Signal D：消费证据（复用 /api/v1/rules，与废弃规则共享）→ 置信度 MEDIUM（booster）
    未被任何告警规则引用
    未被任何记录规则引用
    ──────────────────────────────────────────────────
    注意：不排除 Grafana 等外部消费，此信号单独不能判定冗余
    被规则引用 → 反证输出，降低误报风险

  Signal E：历史行为（/api/v1/query 批量查询）           → 置信度 HIGH
    仅对候选集执行（Phase 1 / Signal D 命中 → 纳入候选），
    避免全量扫描（候选上限 200 条，每批 20 条）

    检测内容：
      count_over_time(metric[5m]) 无结果
        → IsStale=true：近 5 分钟无新样本，序列已 stale   → HIGH

      max_over_time(metric[7d]) == 0
        → IsAlwaysZero=true：7 天内恒为零，无业务意义    → HIGH

      changes(metric[7d]) == 0（非零）
        → IsStatic=true：7 天内值完全静止，无信息量       → HIGH

Phase 3：综合定级

  任意 HIGH 信号（历史行为）                            → 整体置信度 HIGH
  Signal A（环境标签）单独命中                          → 整体置信度 MEDIUM
  Signal D（booster）+ 任意 LOW 叠加                   → 整体置信度 MEDIUM
  ≥2 个独立 LOW 信号（B + C）                          → 整体置信度 MEDIUM
  仅 1 个 LOW 信号                                     → 整体置信度 LOW
  仅有反证（被规则引用），无任何正向信号                 → 不标记
```

**候选预筛策略**（避免全量历史查询）

Signal E 查询有 API 开销，只对满足以下任意条件的指标执行：
- 名称命中 `redundant_patterns` 正则
- 携带测试环境标签
- 未被任何 Prometheus 规则引用

**置信度对应行动建议**

| 置信度 | 建议 |
|--------|------|
| HIGH | 可直接标记为待清理，与 exporter 负责人确认后删除 |
| MEDIUM | 检查 Grafana Dashboard 是否引用，确认无消费后清理 |
| LOW | 仅作观察，需业务上下文确认 |

**输出示例**
```
# HIGH 置信度（历史行为证据）
[minor] debug_trace_counter
  ↳ [置信度:中] 环境标签 env="dev" 表明该指标属于非生产环境
  ↳ 近 7 天最大值为 0，长期无业务意义（历史行为证据）
  ↳ 未被任何 Prometheus 告警/记录规则引用

# MEDIUM 置信度（环境标签）
[minor] test_payment_mock
  ↳ [置信度:中] 环境标签 env="dev" 表明该指标属于非生产环境（环境证据）
  ↳ [置信度:低] 名称匹配疑似冗余模式 "^test_"（弱信号，仅名称启发）

# LOW 置信度（仅名称启发）
[minor] tmp_migration_flag
  ↳ [置信度:低] 名称匹配疑似冗余模式 "^tmp_"（弱信号，仅名称启发，需人工复核）
```

**配置项**：`detection.redundant_patterns`

**已知局限性**

| 局限性 | 说明 |
|--------|------|
| Grafana 消费盲区 | Signal D 不覆盖 Grafana Dashboard 查询 |
| 历史查询有上限 | 候选超过 200 条时截断，超出部分仅做静态分析 |
| value=0 的语义二义性 | 值为 0 可能是业务正常状态（如错误计数），历史行为证据需结合上下文 |
| 语义重叠未实现 | 是否与另一指标在语义上完全重叠（可被替代）尚未实现 |
| 消费证据时效性 | Grafana 使用情况、手动 PromQL 查询不在覆盖范围 |
| 误报来源 | `test_coverage_ratio`（合法质量指标）等可能误报 |

---

## 四、置信度信号汇总表

| 类型 | Signal | 置信度 | 模式 |
|------|--------|--------|------|
| 疑似废弃 | HELP 文本显式废弃声明 | HIGH | 双模式 |
| 疑似废弃 | 生命周期标签（deprecated/sunset_date/replaced_by） | HIGH | 双模式 |
| 疑似废弃 | 未被任何 Prometheus 规则引用 | MEDIUM booster | Prometheus 模式 |
| 疑似废弃 | 名称前缀/后缀/关键字启发 | LOW | 双模式 |
| 疑似冗余 | 历史行为：stale/always-zero/static（7d） | HIGH | Prometheus 模式 |
| 疑似冗余 | 环境标签（env=test/dev/debug 等） | MEDIUM 直接 | 双模式 |
| 疑似冗余 | 未被任何 Prometheus 规则引用 | MEDIUM booster | Prometheus 模式 |
| 疑似冗余 | 名称启发式（test_/debug_/tmp_ 等） | LOW | 双模式 |
| 疑似冗余 | 完全无 Label | LOW | 双模式 |

---

## 五、未来可扩展的检测能力

以下能力因依赖外部系统或计算成本较高，当前版本未实现，列出供参考：

| 能力 | 所需依赖 | 适用类型 |
|------|---------|---------|
| Grafana Dashboard 消费证据 | Grafana HTTP API | 废弃 / 冗余 |
| 用户查询历史消费证据 | Prometheus query log（`--query.log-file`） | 废弃 / 冗余 |
| 语义重叠检测（Label 指纹 + 名称相似度） | 无额外依赖，纯内存计算 | 新类型：重叠指标 |
| 时序相关性重叠（Pearson 相关系数） | `/api/v1/query_range` | 新类型：重叠指标 |
| LLM 语义重叠分析 | 现有 LLMProvider 接口 | 新类型：重叠指标 |
| 废弃指标值趋势分析（是否正在下降） | `/api/v1/query_range` | 废弃 |
| 替换关系自动验证（replaced_by 指向的新指标是否存在） | 当前已有 metrics 数据 | 废弃 |
