# AI 分析模块改进方向

## 一、当前实现概述

### 1.1 调用架构

```
StatisticsReport + []InvalidMetric
        │
        ▼
   Phase 1 Prompt ──► LLM ──► 根因分析文本（Markdown）
        │
        ▼
   Phase 2 Prompt ──► LLM ──► 治理报告文本（Markdown）
        │
        ▼
   AIAnalysisResult
   ├── RiskSummary      = phase1 原始文本
   └── GovernanceReport = phase2 原始文本
```

### 1.2 当前传给 LLM 的信息

**Phase 1 输入**：
- 指标总数、有效/无效数量及占比
- 各类型无效指标数量分布
- 三级风险分布（Critical / Major / Minor 各多少条）
- Top10 高风险指标：仅有指标名、风险等级、问题描述（截断至 80 字符）

**Phase 2 输入**：
- Phase 1 原始输出文本（完整拼入）
- 总指标数、无效率
- 三级风险数量

---

## 二、当前问题分析

### 问题 1：传给 AI 的信息粒度太粗

LLM 只能看到聚合统计和指标名，**无法看到**以下关键信息：

| 缺失信息 | 影响 |
|---------|------|
| 指标的完整 Labels | 无法判断是哪个服务/环境的问题，无法给出针对性建议 |
| 置信度（HIGH / MEDIUM / LOW） | AI 无法区分"已确认问题"和"疑似问题"，可能对弱信号过度分析 |
| 检测证据详情 | HELP 文本内容、生命周期标签值等上下文丢失，根因分析只能泛泛而谈 |
| 指标的 HELP 文本 | LLM 自己无法了解指标的业务含义 |
| 确定性 vs 候选类型区分 | 混在一起分析，治理建议缺乏针对性 |

**结果**：LLM 给出的根因只能是方向性判断（"可能是服务下线未清理"），无法具体到"哪个服务、哪批指标、建议如何操作"。

### 问题 2：Phase 2 完全依赖 Phase 1 的原始文本

Phase 2 把 Phase 1 输出文本原文拼入新 Prompt，导致：
- **Token 浪费**：Phase 1 内容完整重复一遍，第二次调用 Token 消耗接近翻倍
- **无结构传递**：Phase 2 对 Phase 1 的结论没有语义解析，只是文本追加，LLM 需要重新理解 Phase 1 的输出
- **链路不清晰**：两次调用之间的依赖关系是隐式的，难以调试和优化

### 问题 3：输出无结构化，下游无法程序化处理

当前 `AIAnalysisResult` 的核心字段：
```go
RiskSummary      string  // Phase 1 原始 Markdown
GovernanceReport string  // Phase 2 原始 Markdown
```

问题：
- 健康评分（0-100）是嵌在 Markdown 文本里的，无法直接读取数字
- 建议列表无法按优先级排序或过滤
- Relabel 规则片段散落在文本中，无法直接提取使用
- 根因列表无法关联到具体指标记录

### 问题 4：两种无效指标类型未区分处理

当前 Prompt 把所有无效指标混在一起分析。但：
- **确定性问题**（高基数、空值、违规标签、重复、孤儿）：有确凿证据，应给出确定性的整改措施
- **候选问题**（疑似废弃、疑似冗余）：存在误报可能，应给出"先确认再操作"的建议

混合分析会导致 AI 对疑似废弃给出"立即删除"这样激进的建议，或对高基数给出过于保守的"观察"建议。

---

## 三、改进方向

### 改进 1：丰富传入 LLM 的指标上下文

**目标**：让 LLM 看到足够的指标详情，给出针对具体指标的可操作建议。

**方案**：构建结构化指标摘要，传入 Top20 每条的完整上下文：

```
指标：payment_user_transaction
  类型：高基数（Label 组合数 5000，超出阈值 50 倍，Major）
  标签：{user_id, tx_id, env="prod"}
  高危标签：user_id（用户 ID，高散列值）
  建议关注：考虑移除 user_id 标签，改用聚合维度

指标：old_payment_gateway_requests
  类型：疑似废弃（置信度 HIGH）
  证据：HELP 文本 = "DEPRECATED: use payment_request_total instead."
  关联新指标：payment_request_total（已存在）
  建议关注：可直接下线，替代指标已存在
```

相比当前的"截断至 80 字符的问题描述"，LLM 能给出更精准的分析。

### 改进 2：按无效类型分组，构造类型专属 Prompt

**目标**：不同类型的问题采用不同的分析视角，避免混合分析导致建议失焦。

**方案**：将指标按问题类型分组，为每类构造专项 Prompt：

| 分组 | 分析视角 | 典型输出 |
|------|---------|---------|
| 高基数 | 哪些标签造成基数爆炸，如何做标签归一化 | 具体的 relabel 规则 |
| 违规标签/空值 | exporter 配置问题，通知哪个团队整改 | 整改优先级清单 |
| 孤儿指标 | 哪些服务已下线，清理窗口建议 | 分批清理计划 |
| 疑似废弃 | 区分高/中/低置信度，差异化处置 | 按置信度分层的操作建议 |
| 疑似冗余 | 是否有活跃消费，是否有历史价值 | 建议下线 or 保留观察 |

### 改进 3：结构化输出替代纯 Markdown

**目标**：LLM 输出可被程序读取的结构化结果，支持后续排序、过滤、统计。

**方案**：要求 LLM 返回 JSON，在程序侧解析后再渲染为报表：

```json
{
  "overall_score": 42,
  "health_level": "差",
  "score_deductions": [
    {"reason": "高基数指标 5 条，最严重超出阈值 5000 倍", "points": -30},
    {"reason": "3 条违规标签可能导致 scrape 失败", "points": -20}
  ],
  "root_causes": [
    {
      "category": "标签设计缺陷",
      "description": "user_id、tx_id 等高散列值标签直接用于指标维度",
      "affected_metrics": ["payment_user_transaction"],
      "severity": "critical"
    }
  ],
  "suggestions": [
    {
      "priority": 1,
      "action": "移除 payment_user_transaction 的 user_id 标签",
      "detail": "通过 relabel 规则删除，改用 sum by(env) 聚合",
      "relabel_rule": "- source_labels: [__name__]\n  regex: payment_user_transaction\n  action: labeldrop\n  regex: user_id"
    }
  ]
}
```

**优点**：
- 健康评分可直接读取数字，用于趋势图、告警
- 建议列表可按优先级排序展示
- Relabel 规则可直接提取到配置文件
- 根因可关联到具体指标，报表中做交叉引用

### 改进 4：优化两阶段调用结构

**目标**：减少 Token 浪费，提升调用效率。

**方案**：Phase 1 返回结构化 JSON，Phase 2 只接收结构化摘要而非原文：

```
Phase 1：给定指标详情 → 输出结构化根因 JSON
  输入：Top20 完整上下文（含证据、Labels）
  输出：JSON { root_causes[], pattern_summary, health_level }

Phase 2：给定根因 JSON + 统计摘要 → 输出治理建议 JSON
  输入：Phase 1 的 root_causes[] 精简版（不重复传原始指标详情）
  输出：JSON { score, score_deductions[], suggestions[], relabel_rules[] }
```

**Token 节省估算**：Phase 2 不再重传 Phase 1 的分析文本（通常 1000-3000 token），改为只传结构化摘要（约 300-500 token）。

### 改进 5：置信度感知的分析策略

**目标**：让 LLM 对不同置信度的问题给出合适力度的建议。

**方案**：在 Prompt 中明确区分三档，要求 LLM 按档给出差异化建议：

```
请按以下策略给出建议：
- 确定性问题（高基数/空值/违规标签/重复/孤儿）：
  给出直接可执行的操作步骤，无需额外确认。
  
- 疑似问题（置信度 HIGH）：
  给出"建议确认后操作"的步骤，包括确认方式。
  
- 疑似问题（置信度 MEDIUM/LOW）：
  只给出"如何进一步调查"的方向，不给出直接操作建议。
  明确说明"此结论需人工复核"。
```

---

## 四、改进优先级

| 优先级 | 改进项 | 难度 | 收益 |
|--------|--------|------|------|
| P1 | **结构化输出**（返回 JSON 而非纯 Markdown） | 低 | 高：解锁所有下游处理能力 |
| P1 | **丰富指标上下文**（传完整 Labels、证据、HELP 文本） | 低 | 高：根因分析精准度大幅提升 |
| P2 | **置信度感知分析**（区分确定性 vs 候选，给出差异化建议） | 低 | 中：减少误导性建议 |
| P2 | **优化两阶段结构**（Phase 2 接收结构化摘要而非原文） | 中 | 中：降低 Token 成本，提升速度 |
| P3 | **按类型分组 Prompt**（高基数/废弃/冗余各自专项分析） | 中 | 中：类型专属建议质量更高 |

---

## 五、改进后的预期效果

**改进前（当前）**：
```
根因分析：
可能存在服务下线未清理的问题，建议检查废弃指标。
高基数指标可能导致性能问题，建议优化标签设计。
```

**改进后（目标）**：
```
根因分析：
1. [确认问题] payment_user_transaction 存在高基数风险
   - 证据：user_id 标签产生 5000 个唯一 Series，超出阈值 50 倍
   - 根因：业务方将用户 ID 直接用于指标标签维度
   - 建议（P1）：添加 relabel 规则删除 user_id 标签
     metric_relabel_configs:
       - source_labels: [__name__]
         regex: payment_user_transaction
         action: labeldrop
         regex: user_id

2. [确认废弃] old_payment_gateway_requests
   - 证据：HELP 文本明确标注 DEPRECATED，替代指标 payment_request_total 已存在
   - 建议（P1）：直接下线，通知 payment 团队在 scrape config 中移除

3. [疑似冗余，置信度 MEDIUM，建议先确认] tmp_migration_flag
   - 证据：未被任何规则引用，当前值为 0
   - 建议：先排查 Grafana Dashboard 是否引用，无引用后再决定是否删除
```
