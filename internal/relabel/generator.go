package relabel

import (
	"fmt"
	"strings"
	"time"

	"github.com/memo/prometheus-analyzer/internal/model"
)

// RuleCategory 规则分类
type RuleCategory string

const (
	CategoryDropMetric  RuleCategory = "drop_metric"  // 清除整条指标序列
	CategoryDropLabel   RuleCategory = "drop_label"   // 清除高基数或违规 Label
	CategoryAuditNeeded RuleCategory = "audit_needed" // 置信度不足，注释输出，需人工审核
)

// GeneratedRule 一组逻辑相关的 relabel 配置条目
type GeneratedRule struct {
	Category    RuleCategory
	MetricName  string // 目标指标名（仅做展示/注释用）
	Description string // 规则说明
	Evidence    string // 检测证据摘要
	Confidence  string // confirmed / high / medium（low 不生成）
	Lines       []string // YAML 行（不含缩进前缀）
	Disabled    bool    // true = medium 置信度，输出时每行以 # 注释
}

// RuleSet 本次扫描生成的全部规则
type RuleSet struct {
	GeneratedAt  string
	TotalRules   int
	DisabledRules int // 注释掉的（需审核）规则数
	Rules        []GeneratedRule
}

// Generator relabel 规则生成器
type Generator struct{}

func New() *Generator { return &Generator{} }

// Generate 从检测结果生成 relabel 规则集合
func (g *Generator) Generate(invalids []model.InvalidMetric) RuleSet {
	// 先对同名指标去重：保留问题最严重的那条
	byName := make(map[string]*model.InvalidMetric)
	for i := range invalids {
		inv := &invalids[i]
		existing, ok := byName[inv.Metric.Name]
		if !ok {
			cp := *inv
			byName[inv.Metric.Name] = &cp
			continue
		}
		if model.SeverityOrder[inv.MaxSeverity] > model.SeverityOrder[existing.MaxSeverity] ||
			len(inv.Issues) > len(existing.Issues) {
			cp := *inv
			byName[inv.Metric.Name] = &cp
		}
	}

	var rules []GeneratedRule
	for _, inv := range byName {
		generated := g.generateForMetric(*inv)
		rules = append(rules, generated...)
	}

	disabled := 0
	for _, r := range rules {
		if r.Disabled {
			disabled++
		}
	}

	return RuleSet{
		GeneratedAt:   time.Now().Format("2006-01-02 15:04:05"),
		TotalRules:    len(rules),
		DisabledRules: disabled,
		Rules:         rules,
	}
}

func (g *Generator) generateForMetric(inv model.InvalidMetric) []GeneratedRule {
	var rules []GeneratedRule

	// drop 类规则按指标名去重：同一指标只保留置信度最高的那条
	var bestDrop *GeneratedRule

	for _, issue := range inv.Issues {
		switch issue.Type {

		case model.TypeHighCardinality:
			if r := g.highCardinalityRule(inv, issue); r != nil {
				rules = append(rules, *r)
			}

		case model.TypeCandidateDeprecated:
			r := g.dropMetricRule(inv, issue, "疑似废弃")
			bestDrop = pickBetterDrop(bestDrop, r)

		case model.TypeCandidateRedundant:
			r := g.dropMetricRule(inv, issue, "疑似冗余")
			bestDrop = pickBetterDrop(bestDrop, r)

		case model.TypeOrphan:
			r := g.orphanDropRule(inv, issue)
			bestDrop = pickBetterDrop(bestDrop, r)

		case model.TypeIllegalLabel:
			rules = append(rules, g.illegalLabelRules(inv, issue)...)
		}
		// TypeDuplicate / TypeEmpty 不生成（需修 exporter）
	}

	if bestDrop != nil {
		rules = append(rules, *bestDrop)
	}
	return rules
}

// pickBetterDrop 两条 drop 规则中保留置信度更高的（已有 > 待选）
func pickBetterDrop(existing, candidate *GeneratedRule) *GeneratedRule {
	if candidate == nil {
		return existing
	}
	if existing == nil {
		return candidate
	}
	// 未注释（confirmed/high）优先于已注释（medium）
	if existing.Disabled && !candidate.Disabled {
		return candidate
	}
	return existing
}

// ── 规则生成：各类型 ──────────────────────────────────────────────

func (g *Generator) highCardinalityRule(inv model.InvalidMetric, issue model.DetectedIssue) *GeneratedRule {
	highCardKeys, allKeys := classifyLabelKeys(inv.Metric.Labels)

	var lines []string
	var keyDesc string

	if len(highCardKeys) > 0 {
		// 有明确高基数嫌疑的 label
		keyDesc = fmt.Sprintf("高基数嫌疑标签：%s（其余标签：%s）",
			strings.Join(highCardKeys, ", "), strings.Join(without(allKeys, highCardKeys), ", "))
		for _, k := range highCardKeys {
			lines = append(lines, dropLabelFromMetricLines(inv.Metric.Name, k)...)
		}
	} else {
		// 无法判断，列出全部 label 供用户选择
		keyDesc = fmt.Sprintf("全部标签：%s（请确认哪些是高基数 label 后启用对应行）",
			strings.Join(allKeys, ", "))
		for _, k := range allKeys {
			lines = append(lines, dropLabelFromMetricLines(inv.Metric.Name, k)...)
		}
	}

	return &GeneratedRule{
		Category:    CategoryDropLabel,
		MetricName:  inv.Metric.Name,
		Description: issue.Description,
		Evidence:    keyDesc,
		Confidence:  "confirmed",
		Lines:       lines,
		Disabled:    false,
	}
}

func (g *Generator) dropMetricRule(inv model.InvalidMetric, issue model.DetectedIssue, typeName string) *GeneratedRule {
	conf := string(issue.Confidence)
	if issue.Confidence == model.ConfidenceLow {
		return nil // 低置信度不生成
	}

	disabled := issue.Confidence == model.ConfidenceMedium

	lines := []string{
		fmt.Sprintf(`- source_labels: [__name__]`),
		fmt.Sprintf(`  regex: %s`, inv.Metric.Name),
		`  action: drop`,
	}

	desc := fmt.Sprintf("%s（置信度：%s）", typeName, conf)
	if disabled {
		desc += "  ← 置信度中等，已注释，需人工审核后启用"
	}

	return &GeneratedRule{
		Category:    CategoryDropMetric,
		MetricName:  inv.Metric.Name,
		Description: desc,
		Evidence:    firstNonEmpty(issue.Description, inv.Metric.HelpText),
		Confidence:  conf,
		Lines:       lines,
		Disabled:    disabled,
	}
}

func (g *Generator) orphanDropRule(inv model.InvalidMetric, issue model.DetectedIssue) *GeneratedRule {
	// 动态模式（描述中含"不在 Prometheus 存活 target"）可信度更高
	isDynamic := strings.Contains(issue.Description, "存活 target")
	disabled := !isDynamic

	lines := []string{
		`- source_labels: [__name__]`,
		fmt.Sprintf(`  regex: %s`, inv.Metric.Name),
		`  action: drop`,
	}

	conf := "high"
	if !isDynamic {
		conf = "medium"
	}
	desc := "孤儿指标"
	if disabled {
		desc += "（静态白名单模式，置信度中等，已注释，需确认服务确已下线）"
	}

	return &GeneratedRule{
		Category:    CategoryDropMetric,
		MetricName:  inv.Metric.Name,
		Description: desc,
		Evidence:    issue.Description,
		Confidence:  conf,
		Lines:       lines,
		Disabled:    disabled,
	}
}

func (g *Generator) illegalLabelRules(inv model.InvalidMetric, issue model.DetectedIssue) []GeneratedRule {
	// 从 issue.Description 中提取违规的 label key
	illegalKeys := extractIllegalKeys(inv.Metric.Labels, issue.Description)
	if len(illegalKeys) == 0 {
		return nil
	}

	var rules []GeneratedRule
	for _, k := range illegalKeys {
		lines := []string{
			`- action: labeldrop`,
			fmt.Sprintf(`  regex: "%s"`, k),
		}
		rules = append(rules, GeneratedRule{
			Category:    CategoryDropLabel,
			MetricName:  inv.Metric.Name,
			Description: fmt.Sprintf("删除违规 Label Key '%s'（含非法字符或保留前缀）", k),
			Evidence:    issue.Description,
			Confidence:  "confirmed",
			Lines:       lines,
			Disabled:    false,
		})
	}
	return rules
}

// ── YAML 渲染 ─────────────────────────────────────────────────────

// ToYAML 将规则集合渲染为可直接复制到 prometheus.yml 的 YAML 字符串
// indent 为每行前缀缩进（通常为 "  " 两空格，置于 metric_relabel_configs 下）
func (rs *RuleSet) ToYAML(indent string) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# 自动生成的 metric_relabel_configs 规则\n"))
	sb.WriteString(fmt.Sprintf("# 生成时间：%s\n", rs.GeneratedAt))
	sb.WriteString(fmt.Sprintf("# 共 %d 条规则，其中 %d 条已注释（需人工审核后启用）\n",
		rs.TotalRules, rs.DisabledRules))
	sb.WriteString("# 使用方式：将以下内容添加到 scrape_config 的 metric_relabel_configs 下\n#\n")

	// 按分类分组输出
	categories := []struct {
		cat   RuleCategory
		title string
	}{
		{CategoryDropLabel, "── 高基数标签清理 / 违规标签删除"},
		{CategoryDropMetric, "── 废弃/冗余/孤儿指标清理"},
		{CategoryAuditNeeded, "── 待审核规则"},
	}

	for _, g := range categories {
		var group []GeneratedRule
		for _, r := range rs.Rules {
			if r.Category == g.cat {
				group = append(group, r)
			}
		}
		if len(group) == 0 {
			continue
		}

		sb.WriteString(fmt.Sprintf("\n# %s\n", g.title))
		for _, rule := range group {
			sb.WriteString(renderRule(rule, indent))
		}
	}

	return sb.String()
}

func renderRule(rule GeneratedRule, indent string) string {
	var sb strings.Builder
	// meta 注释行前缀（含缩进）
	meta := indent + "# "

	if rule.Disabled {
		// 整块注释掉：元数据 + 规则行均加 # 前缀
		sb.WriteString(fmt.Sprintf("%s[需审核] %s — %s\n", meta, rule.MetricName, rule.Description))
		sb.WriteString(fmt.Sprintf("%s证据：%s\n", meta, truncate(rule.Evidence, 100)))
		sb.WriteString(fmt.Sprintf("%s启用方式：删除每行行首的 '# ' 注释符\n", meta))
		for _, line := range rule.Lines {
			sb.WriteString(fmt.Sprintf("%s# %s\n", indent, line))
		}
	} else {
		// 元数据用注释，规则行直接输出
		sb.WriteString(fmt.Sprintf("%s%s — %s\n", meta, rule.MetricName, rule.Description))
		if rule.Evidence != "" {
			sb.WriteString(fmt.Sprintf("%s证据：%s\n", meta, truncate(rule.Evidence, 100)))
		}
		for _, line := range rule.Lines {
			sb.WriteString(fmt.Sprintf("%s%s\n", indent, line))
		}
	}
	sb.WriteString("\n")
	return sb.String()
}

// ── 辅助函数 ──────────────────────────────────────────────────────

// dropLabelFromMetricLines 生成"仅对特定指标清空某 label"的 relabel 规则行
// 原理：当 __name__ 匹配指定指标时，将目标 label 替换为空字符串
func dropLabelFromMetricLines(metricName, labelKey string) []string {
	return []string{
		`- source_labels: [__name__]`,
		fmt.Sprintf(`  regex: %s`, metricName),
		fmt.Sprintf(`  target_label: %s`, labelKey),
		`  replacement: ""`,
		`  action: replace`,
	}
}

// heuristicHighCardPatterns 已知的高基数 label 名称模式
var heuristicHighCardPatterns = []string{
	"user_id", "userid", "uid", "user",
	"trace_id", "traceid", "traceid", "trace",
	"span_id", "spanid",
	"request_id", "requestid", "req_id", "reqid",
	"tx_id", "txid", "transaction_id",
	"session_id", "sessionid",
	"pod", "pod_name", "container",
	"path", "url", "uri", "endpoint",
	"query", "sql", "statement",
	"build_id", "commit", "version_hash",
}

// classifyLabelKeys 将 label 键分为"高基数嫌疑"和"其他"两组
func classifyLabelKeys(labels []model.Label) (highCard, all []string) {
	for _, l := range labels {
		all = append(all, l.Key)
		if isHighCardKey(l.Key) || isHighCardValue(l.Value) {
			highCard = append(highCard, l.Key)
		}
	}
	return
}

func isHighCardKey(key string) bool {
	lk := strings.ToLower(key)
	for _, p := range heuristicHighCardPatterns {
		if lk == p || strings.HasSuffix(lk, "_"+p) || strings.HasSuffix(lk, p+"_id") {
			return true
		}
	}
	return false
}

// isHighCardValue 根据 value 特征推断是否为高基数（ID 类：长度>12 且含数字字母混合）
func isHighCardValue(val string) bool {
	if len(val) < 8 {
		return false
	}
	hasDigit, hasLetter := false, false
	for _, ch := range val {
		if ch >= '0' && ch <= '9' {
			hasDigit = true
		}
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') {
			hasLetter = true
		}
	}
	return hasDigit && hasLetter && len(val) > 12
}

// extractIllegalKeys 从 Issue.Description 中提取违规的 label key
func extractIllegalKeys(labels []model.Label, desc string) []string {
	var keys []string
	seen := make(map[string]struct{})
	for _, l := range labels {
		if _, ok := seen[l.Key]; ok {
			continue
		}
		// 描述中含该 key 名 → 认为是违规 key
		if strings.Contains(desc, "'"+l.Key+"'") ||
			strings.Contains(desc, `"`+l.Key+`"`) {
			keys = append(keys, l.Key)
			seen[l.Key] = struct{}{}
		}
	}
	return keys
}

func without(all, exclude []string) []string {
	excSet := make(map[string]struct{}, len(exclude))
	for _, e := range exclude {
		excSet[e] = struct{}{}
	}
	var result []string
	for _, a := range all {
		if _, ok := excSet[a]; !ok {
			result = append(result, a)
		}
	}
	return result
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
