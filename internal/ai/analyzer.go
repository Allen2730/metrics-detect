package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/memo/prometheus-analyzer/internal/model"
	"github.com/memo/prometheus-analyzer/pkg/logger"
	"go.uber.org/zap"
)

// confirmedTypes 确定性问题类型，有明确证据，AI 可直接给出整改建议
var confirmedTypes = map[model.InvalidType]struct{}{
	model.TypeHighCardinality: {},
	model.TypeEmpty:           {},
	model.TypeIllegalLabel:    {},
	model.TypeDuplicate:       {},
	model.TypeOrphan:          {},
}

// Analyzer 编排两阶段 AI 分析
type Analyzer struct {
	provider LLMProvider
	model    string
	timeout  int
}

func NewAnalyzer(provider LLMProvider, modelName string, timeoutSec int) *Analyzer {
	return &Analyzer{provider: provider, model: modelName, timeout: timeoutSec}
}

// phase1Response Phase 1 LLM 返回的 JSON 结构
type phase1Response struct {
	HealthLevel   string          `json:"health_level"`
	HealthSummary string          `json:"health_summary"`
	MainPatterns  []model.Pattern `json:"main_patterns"`
}

// phase2Response Phase 2 LLM 返回的 JSON 结构
type phase2Response struct {
	OverallScore    int                `json:"overall_score"`
	HealthLevel     string             `json:"health_level"`
	ScoreDeductions []model.Deduction  `json:"score_deductions"`
	Suggestions     []model.Suggestion `json:"suggestions"`
	RelabelRules    []model.RelabelRule `json:"relabel_rules"`
}

func (a *Analyzer) Analyze(stats model.StatisticsReport, invalids []model.InvalidMetric) (model.AIAnalysisResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(a.timeout)*time.Second)
	defer cancel()

	result := model.AIAnalysisResult{
		Provider: a.provider.Name(),
		Model:    a.model,
	}

	logger.Info("starting AI analysis", zap.String("provider", a.provider.Name()))

	// ── Phase 1：根因分析 ─────────────────────────────────────────
	phase1Prompt := buildPhase1Prompt(stats)
	phase1Raw, err := a.provider.Analyze(ctx, phase1Prompt)
	if err != nil {
		return result, fmt.Errorf("phase1 AI analysis: %w", err)
	}
	logger.Debug("phase1 AI done", zap.Int("response_len", len(phase1Raw)))

	var p1 phase1Response
	if parseErr := parseJSON(phase1Raw, &p1); parseErr != nil {
		logger.Warn("phase1 JSON parse failed, storing raw", zap.Error(parseErr))
		result.RawPhase1 = phase1Raw
	} else {
		result.HealthLevel = p1.HealthLevel
		result.HealthSummary = p1.HealthSummary
		result.MainPatterns = p1.MainPatterns
	}

	// ── Phase 2：治理建议 + 评分 ──────────────────────────────────
	// 传入 Phase 1 的结构化结果（非原始文本），减少 Token 消耗
	phase2Prompt := buildPhase2Prompt(stats, p1)
	phase2Raw, err := a.provider.Analyze(ctx, phase2Prompt)
	if err != nil {
		return result, fmt.Errorf("phase2 AI analysis: %w", err)
	}
	logger.Debug("phase2 AI done", zap.Int("response_len", len(phase2Raw)))

	var p2 phase2Response
	if parseErr := parseJSON(phase2Raw, &p2); parseErr != nil {
		logger.Warn("phase2 JSON parse failed, storing raw", zap.Error(parseErr))
		result.RawPhase2 = phase2Raw
	} else {
		result.OverallScore = p2.OverallScore
		if result.HealthLevel == "" {
			result.HealthLevel = p2.HealthLevel
		}
		result.ScoreDeductions = p2.ScoreDeductions
		result.Suggestions = p2.Suggestions
		result.RelabelRules = p2.RelabelRules
	}

	return result, nil
}

// buildPhase1Prompt 构造根因分析 Prompt，含丰富指标上下文，要求 JSON 输出
func buildPhase1Prompt(stats model.StatisticsReport) string {
	var sb strings.Builder

	sb.WriteString("你是一名 Prometheus 监控治理专家。请根据以下指标扫描结果进行深度根因分析。\n\n")

	// 统计概览
	sb.WriteString("## 一、扫描统计概览\n\n")
	sb.WriteString(fmt.Sprintf("- 指标总数：%d，有效：%d，无效：%d（%.1f%%）\n\n",
		stats.TotalMetrics, stats.ValidMetrics, stats.InvalidMetrics, stats.InvalidPercent))

	sb.WriteString("**分类分布：**\n\n")
	for t, ts := range stats.ByType {
		sb.WriteString(fmt.Sprintf("- %s：%d 条（%.1f%%）\n",
			model.InvalidTypeDesc[t], ts.Count, ts.Percent))
	}
	sb.WriteString(fmt.Sprintf("\n**风险分布：** 严重 %d 条 / 一般 %d 条 / 轻微 %d 条\n\n",
		stats.BySeverity[model.SeverityCritical],
		stats.BySeverity[model.SeverityMajor],
		stats.BySeverity[model.SeverityMinor]))

	// 确定性问题详情
	var confirmed, candidates []model.InvalidMetric
	for _, inv := range stats.Top20Invalid {
		if isConfirmed(inv) {
			confirmed = append(confirmed, inv)
		} else {
			candidates = append(candidates, inv)
		}
	}

	if len(confirmed) > 0 {
		sb.WriteString("## 二、确定性问题（有明确证据）\n\n")
		for _, inv := range confirmed {
			writeConfirmedMetric(&sb, inv)
		}
	}

	if len(candidates) > 0 {
		sb.WriteString("## 三、候选问题（多信号推断，需复核）\n\n")
		for _, inv := range candidates {
			writeCandidateMetric(&sb, inv)
		}
	}

	// JSON 输出要求
	sb.WriteString("## 四、输出要求\n\n")
	sb.WriteString("请**仅输出**如下 JSON，不要包含任何其他文字或 markdown 代码块标记：\n\n")
	sb.WriteString("{\n")
	sb.WriteString(`  "health_level": "极差",` + "\n")
	sb.WriteString(`  "health_summary": "一句话说明整体健康状况及最严重的问题",` + "\n")
	sb.WriteString(`  "main_patterns": [` + "\n")
	sb.WriteString(`    {` + "\n")
	sb.WriteString(`      "pattern": "问题模式名称（简短）",` + "\n")
	sb.WriteString(`      "root_cause": "该模式的根因分析（1-3句话）",` + "\n")
	sb.WriteString(`      "affected_metrics": ["metric_name_1", "metric_name_2"],` + "\n")
	sb.WriteString(`      "severity": "critical",` + "\n")
	sb.WriteString(`      "confidence": "confirmed"` + "\n")
	sb.WriteString(`    }` + "\n")
	sb.WriteString(`  ]` + "\n")
	sb.WriteString("}\n\n")
	sb.WriteString("字段约束：\n")
	sb.WriteString("- health_level 取值：好 / 一般 / 差 / 极差\n")
	sb.WriteString("- severity 取值：critical / major / minor\n")
	sb.WriteString("- confidence 取值规则（重要）：\n")
	sb.WriteString("  - \"confirmed\"：模式来自确定性检测（高基数/空值/违规标签/重复/孤儿），有明确证据\n")
	sb.WriteString("  - \"high\"：模式来自候选检测且证据充分（HELP 文本声明废弃 / 生命周期标签 / 历史行为为零）\n")
	sb.WriteString("  - \"medium\"：候选检测中等证据（环境标签 / 未被规则引用）\n")
	sb.WriteString("  - \"low\"：仅名称启发式匹配，无其他证据\n")
	sb.WriteString("- main_patterns 列出 3-5 个主要模式，每个 affected_metrics 列出涉及的真实指标名\n")

	return sb.String()
}

// buildPhase2Prompt 构造治理方案 Prompt，接收 Phase 1 结构化结果，要求 JSON 输出
func buildPhase2Prompt(stats model.StatisticsReport, p1 phase1Response) string {
	var sb strings.Builder

	sb.WriteString("你是一名 Prometheus 监控治理专家。请基于以下根因分析结果，给出完整的治理方案。\n\n")

	// Phase 1 结构化摘要（比原文更精简）
	sb.WriteString("## 一、根因分析摘要\n\n")
	sb.WriteString(fmt.Sprintf("**整体健康状态**：%s\n\n", p1.HealthLevel))
	if p1.HealthSummary != "" {
		sb.WriteString(fmt.Sprintf("**摘要**：%s\n\n", p1.HealthSummary))
	}

	if len(p1.MainPatterns) > 0 {
		sb.WriteString("**主要问题模式（含置信度）：**\n\n")
		for i, p := range p1.MainPatterns {
			conf := p.Confidence
			if conf == "" {
				conf = "confirmed"
			}
			sb.WriteString(fmt.Sprintf("%d. **%s** [severity=%s, confidence=%s]\n",
				i+1, p.Name, p.Severity, conf))
			sb.WriteString(fmt.Sprintf("   根因：%s\n", p.RootCause))
			if len(p.AffectedMetrics) > 0 {
				sb.WriteString(fmt.Sprintf("   涉及：%s\n", strings.Join(p.AffectedMetrics, ", ")))
			}
			sb.WriteString("\n")
		}
	} else {
		sb.WriteString("（Phase 1 分析结果解析失败，请根据以下统计数据直接给出治理方案，所有候选问题视为 low confidence）\n\n")
	}

	// 统计数据
	sb.WriteString("## 二、指标统计\n\n")
	sb.WriteString(fmt.Sprintf("总指标数：%d，无效率：%.1f%%，严重：%d / 一般：%d / 轻微：%d\n\n",
		stats.TotalMetrics, stats.InvalidPercent,
		stats.BySeverity[model.SeverityCritical],
		stats.BySeverity[model.SeverityMajor],
		stats.BySeverity[model.SeverityMinor]))

	// JSON Schema
	sb.WriteString("## 三、JSON 输出格式\n\n")
	sb.WriteString("请**仅输出**如下 JSON，不要包含任何其他文字或 markdown 代码块标记：\n\n")
	sb.WriteString(`{` + "\n")
	sb.WriteString(`  "overall_score": 整数(0-100),` + "\n")
	sb.WriteString(`  "health_level": "极差",` + "\n")
	sb.WriteString(`  "score_deductions": [` + "\n")
	sb.WriteString(`    {"reason": "扣分原因", "points": -30, "detail": "具体说明"}` + "\n")
	sb.WriteString(`  ],` + "\n")
	sb.WriteString(`  "suggestions": [` + "\n")
	sb.WriteString(`    {` + "\n")
	sb.WriteString(`      "priority": 1,` + "\n")
	sb.WriteString(`      "action": "操作标题（简短）",` + "\n")
	sb.WriteString(`      "target_metrics": ["metric_name"],` + "\n")
	sb.WriteString(`      "detail": "步骤描述",` + "\n")
	sb.WriteString(`      "timeline": "立即执行",` + "\n")
	sb.WriteString(`      "action_type": "direct_action"` + "\n")
	sb.WriteString(`    }` + "\n")
	sb.WriteString(`  ],` + "\n")
	sb.WriteString(`  "relabel_rules": [` + "\n")
	sb.WriteString(`    {"description": "规则说明", "config": "YAML 内容（\\n 换行）"}` + "\n")
	sb.WriteString(`  ]` + "\n")
	sb.WriteString(`}` + "\n\n")

	// 置信度感知约束
	sb.WriteString("## 四、置信度感知建议生成策略（必须严格遵守）\n\n")
	sb.WriteString("根据模式的 confidence 字段选择 action_type，**禁止越权操作**：\n\n")
	sb.WriteString("| confidence | action_type | detail 要求 | timeline |\n")
	sb.WriteString("|------------|-------------|-------------|----------|\n")
	sb.WriteString("| confirmed | direct_action | 给出具体可执行的操作命令或配置步骤 | 立即执行 / 计划执行 |\n")
	sb.WriteString("| high | confirm_first | 「先验证X，再执行Y」结构，必须含至少一个验证步骤 | 计划执行 |\n")
	sb.WriteString("| medium | investigate_only | 只描述如何调查确认，不给出删除/修改/停止采集等操作 | 调查阶段 |\n")
	sb.WriteString("| low | investigate_only | 只给调查方向，detail 必须注明：不建议直接操作，需人工确认 | 调查阶段 |\n\n")
	sb.WriteString("其他约束：\n")
	sb.WriteString("- overall_score：100 分起，按扣分项扣减\n")
	sb.WriteString("- score_deductions：3-5 个扣分项，points 为负数\n")
	sb.WriteString("- relabel_rules：只为 confirmed 或 high confidence 的指标生成，不为 medium/low 生成\n")
	sb.WriteString("- config 字段换行用 \\n 转义\n")

	return sb.String()
}

// ── JSON 解析工具 ──────────────────────────────────────────────────

// parseJSON 从 LLM 响应中提取并解析 JSON
// LLM 可能将 JSON 包裹在 ```json ... ``` 代码块中，也可能直接返回裸 JSON
func parseJSON(raw string, v any) error {
	extracted := extractJSONString(raw)
	if err := json.Unmarshal([]byte(extracted), v); err != nil {
		return fmt.Errorf("json unmarshal: %w (extracted: %.200s)", err, extracted)
	}
	return nil
}

// extractJSONString 从响应文本中提取 JSON 字符串
func extractJSONString(s string) string {
	s = strings.TrimSpace(s)

	// 尝试提取 ```json ... ``` 代码块
	if idx := strings.Index(s, "```json"); idx >= 0 {
		start := idx + len("```json")
		if end := strings.Index(s[start:], "```"); end >= 0 {
			return strings.TrimSpace(s[start : start+end])
		}
	}

	// 尝试提取 ``` ... ``` 代码块（内容以 { 开头）
	if idx := strings.Index(s, "```"); idx >= 0 {
		start := idx + 3
		if end := strings.Index(s[start:], "```"); end >= 0 {
			candidate := strings.TrimSpace(s[start : start+end])
			if strings.HasPrefix(candidate, "{") {
				return candidate
			}
		}
	}

	// 直接以 { 开头
	if strings.HasPrefix(s, "{") {
		return s
	}

	// 找第一个 { 到最后一个 }
	first := strings.Index(s, "{")
	last := strings.LastIndex(s, "}")
	if first >= 0 && last > first {
		return s[first : last+1]
	}

	return s
}

// ── Prompt 构建辅助函数 ────────────────────────────────────────────

func writeConfirmedMetric(sb *strings.Builder, inv model.InvalidMetric) {
	primaryType := primaryIssueType(inv)
	typeDesc := model.InvalidTypeDesc[primaryType]

	sb.WriteString(fmt.Sprintf("### [%s] `%s` — %s\n\n",
		strings.ToUpper(string(inv.MaxSeverity)), inv.Metric.Name, typeDesc))

	if len(inv.Metric.Labels) > 0 {
		if primaryType == model.TypeHighCardinality {
			keys := make([]string, 0, len(inv.Metric.Labels))
			for _, l := range inv.Metric.Labels {
				keys = append(keys, l.Key)
			}
			sb.WriteString(fmt.Sprintf("- **标签键**：{%s}\n", strings.Join(keys, ", ")))
		} else {
			sb.WriteString(fmt.Sprintf("- **标签**：%s\n", formatLabels(inv.Metric.Labels)))
		}
	}

	if inv.Metric.HelpText != "" {
		help := inv.Metric.HelpText
		if len(help) > 120 {
			help = help[:120] + "..."
		}
		sb.WriteString(fmt.Sprintf("- **HELP**：%s\n", help))
	}

	if primaryType != model.TypeHighCardinality && !math.IsNaN(inv.Metric.Value) {
		sb.WriteString(fmt.Sprintf("- **当前值**：%g\n", inv.Metric.Value))
	}

	sb.WriteString("- **问题**：\n")
	for _, issue := range inv.Issues {
		sb.WriteString(fmt.Sprintf("  - %s\n", issue.Description))
	}
	sb.WriteString("\n")
}

func writeCandidateMetric(sb *strings.Builder, inv model.InvalidMetric) {
	primaryType := primaryIssueType(inv)
	typeDesc := model.InvalidTypeDesc[primaryType]

	overallConf := model.ConfidenceLow
	if len(inv.Issues) > 0 {
		overallConf = inv.Issues[0].Confidence
	}

	sb.WriteString(fmt.Sprintf("### [%s/置信度:%s] `%s` — %s\n\n",
		strings.ToUpper(string(inv.MaxSeverity)),
		confidenceLabel(overallConf),
		inv.Metric.Name,
		typeDesc))

	if len(inv.Metric.Labels) > 0 {
		sb.WriteString(fmt.Sprintf("- **标签**：%s\n", formatLabels(inv.Metric.Labels)))
	}

	if inv.Metric.HelpText != "" {
		help := inv.Metric.HelpText
		if len(help) > 200 {
			help = help[:200] + "..."
		}
		sb.WriteString(fmt.Sprintf("- **HELP**：%s\n", help))
	}

	sb.WriteString("- **证据链**：\n")
	for _, issue := range inv.Issues {
		sb.WriteString(fmt.Sprintf("  - %s%s\n", confidencePrefix(issue.Confidence), issue.Description))
	}

	switch overallConf {
	case model.ConfidenceHigh:
		sb.WriteString("- 建议：证据充分，可确认替代方案后直接处置\n")
	case model.ConfidenceMedium:
		sb.WriteString("- 建议：需与指标 owner 确认后操作\n")
	default:
		sb.WriteString("- 建议：仅名称启发，需人工核实，不建议直接操作\n")
	}
	sb.WriteString("\n")
}

// ── 通用辅助 ──────────────────────────────────────────────────────

func isConfirmed(inv model.InvalidMetric) bool {
	for _, issue := range inv.Issues {
		if _, ok := confirmedTypes[issue.Type]; ok {
			return true
		}
	}
	return false
}

func primaryIssueType(inv model.InvalidMetric) model.InvalidType {
	best := model.SeverityOrder[model.SeverityMinor]
	var result model.InvalidType
	for _, issue := range inv.Issues {
		if model.SeverityOrder[issue.Severity] >= best {
			best = model.SeverityOrder[issue.Severity]
			result = issue.Type
		}
	}
	return result
}

func formatLabels(labels []model.Label) string {
	const limit = 5
	parts := make([]string, 0, limit)
	for i, l := range labels {
		if i >= limit {
			parts = append(parts, fmt.Sprintf("...+%d 个", len(labels)-limit))
			break
		}
		val := l.Value
		if len(val) > 30 {
			val = val[:30] + "..."
		}
		parts = append(parts, fmt.Sprintf(`%s="%s"`, l.Key, val))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func confidenceLabel(c model.Confidence) string {
	switch c {
	case model.ConfidenceHigh:
		return "HIGH"
	case model.ConfidenceMedium:
		return "MEDIUM"
	default:
		return "LOW"
	}
}

func confidencePrefix(c model.Confidence) string {
	switch c {
	case model.ConfidenceHigh:
		return "[HIGH] "
	case model.ConfidenceMedium:
		return "[MEDIUM] "
	default:
		return "[LOW] "
	}
}

func summarizeIssues(issues []model.DetectedIssue) string {
	descs := make([]string, 0, len(issues))
	for _, i := range issues {
		descs = append(descs, i.Description)
	}
	s := strings.Join(descs, "; ")
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}
