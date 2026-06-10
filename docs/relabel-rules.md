# Relabel 规则生成说明

本文档描述 `--relabel` 标志触发的 `metric_relabel_configs` 规则自动生成逻辑。

## 使用方式

```bash
./metrics-detect analyze --relabel
```

执行后在 `cfg.Report.OutputDir`（默认 `output/`）下生成 `relabel_rules.yaml`，可直接复制到 Prometheus scrape_config 的 `metric_relabel_configs` 字段下。

---

## 覆盖的检测类型

| 检测类型 | 生成规则类型 | 说明 |
|---|---|---|
| `HighCardinality` | `drop_label` | 将高基数标签值替换为空字符串，保留序列但去掉高基数维度 |
| `CandidateDeprecated` | `drop_metric` | 按置信度决定是否注释输出 |
| `CandidateRedundant` | `drop_metric` | 同上 |
| `Orphan` | `drop_metric` | 动态模式直接输出，静态白名单模式注释输出 |
| `IllegalLabel` | `drop_label`（labeldrop） | 删除违规 label key |
| `Duplicate` | 不生成 | 需修复 exporter，relabel 无法从根本上解决 |
| `Empty` | 不生成 | 同上 |

---

## 规则结构

### HighCardinality — 清空高基数标签值

```yaml
- source_labels: [__name__]
  regex: <metric_name>
  target_label: <high_card_label_key>
  replacement: ""
  action: replace
```

通过 `action: replace` 将目标 label 的值清空（而非删除整条序列），保留时序数据的可用性。

**自动识别的高基数 label key 模式**（匹配 key 名或其后缀）：

```
user_id  userid  uid  user
trace_id  traceid  trace
span_id  spanid
request_id  requestid  req_id  reqid
tx_id  txid  transaction_id
session_id  sessionid
pod  pod_name  container
path  url  uri  endpoint
query  sql  statement
build_id  commit  version_hash
```

若 label value 长度 > 12 且同时含数字与字母，也判定为高基数值。若无法识别，列出全部 label 供人工选择。

---

### CandidateDeprecated / CandidateRedundant / Orphan — drop 整条序列

```yaml
- source_labels: [__name__]
  regex: <metric_name>
  action: drop
```

**Orphan 补充说明：**

- **动态模式**（通过 `/api/v1/targets` 确认服务不存活）：置信度 `high`，直接输出
- **静态白名单模式**（服务前缀不在 `known_service_names` 配置中）：置信度 `medium`，注释输出，需确认服务确已下线后启用

---

### IllegalLabel — labeldrop 违规 key

```yaml
- action: labeldrop
  regex: "<illegal_key>"
```

从 issue 描述中提取违规的 label key 名（含非法字符、`__` 保留前缀、数字开头等），每个违规 key 生成一条独立规则。

---

## 置信度与注释策略

| 置信度 | 输出方式 |
|---|---|
| `confirmed` / `high` | 直接输出，可立即使用 |
| `medium` | 整块注释（每行加 `# `），需人工审核后去注释启用 |
| `low` | 不生成 |

注释块示例：

```yaml
  # [需审核] some_metric — 疑似废弃（置信度：medium）
  # 证据：HELP 文本含 deprecated 字样
  # 启用方式：删除每行行首的 '# ' 注释符
  # - source_labels: [__name__]
  #   regex: some_metric
  #   action: drop
```

---

## 去重逻辑

同一指标名可能同时命中多条 drop 类规则（如既是 Orphan 又疑似 Deprecated），此时只保留置信度最高的一条（未注释优先于注释）。

`HighCardinality` 的 label drop 规则不参与去重，可与 drop metric 规则并存。

---

## 输出文件格式

规则按分类分组，顺序为：

1. **高基数标签清理 / 违规标签删除**（drop_label 类）
2. **废弃 / 冗余 / 孤儿指标清理**（drop_metric 类）
3. **待审核规则**（audit_needed 类）

文件头包含生成时间、规则总数及待审核数。
