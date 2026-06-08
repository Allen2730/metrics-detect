package reporter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/memo/prometheus-analyzer/internal/model"
)

// MarkdownReporter 输出 Markdown 报表
type MarkdownReporter struct {
	OutputDir string
}

func actionTypeEmoji(at model.ActionType) string {
	switch at {
	case model.ActionTypeDirect:
		return "🔴 直接执行"
	case model.ActionTypeConfirmFirst:
		return "🟡 先确认"
	case model.ActionTypeInvestigate:
		return "🔍 仅调查"
	default:
		return "⬜ 待确认"
	}
}

func NewMarkdownReporter(dir string) *MarkdownReporter {
	return &MarkdownReporter{OutputDir: dir}
}

func (r *MarkdownReporter) Write(stats model.StatisticsReport, ai model.AIAnalysisResult) error {
	if err := os.MkdirAll(r.OutputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	filename := fmt.Sprintf("report_%s.md", time.Now().Format("20060102_150405"))
	path := filepath.Join(r.OutputDir, filename)

	var sb strings.Builder
	sb.WriteString("# Prometheus 无效指标分析报告\n\n")
	sb.WriteString(fmt.Sprintf("> 生成时间：%s\n\n", time.Now().Format("2006-01-02 15:04:05")))

	// 总览
	sb.WriteString("## 一、总览\n\n")
	sb.WriteString("| 指标 | 数量 |\n|------|------|\n")
	sb.WriteString(fmt.Sprintf("| 指标总数 | %d |\n", stats.TotalMetrics))
	sb.WriteString(fmt.Sprintf("| 有效指标 | %d |\n", stats.ValidMetrics))
	sb.WriteString(fmt.Sprintf("| 无效指标 | %d (%.1f%%) |\n\n", stats.InvalidMetrics, stats.InvalidPercent))

	// 分类统计
	sb.WriteString("## 二、无效指标分类统计\n\n")
	sb.WriteString("| 类型 | 数量 | 占比 |\n|------|------|------|\n")
	typeOrder := []model.InvalidType{
		model.TypeHighCardinality, model.TypeEmpty, model.TypeIllegalLabel,
		model.TypeDuplicate, model.TypeOrphan,
		model.TypeCandidateRedundant, model.TypeCandidateDeprecated,
	}
	for _, t := range typeOrder {
		ts, ok := stats.ByType[t]
		if !ok {
			continue
		}
		sb.WriteString(fmt.Sprintf("| %s | %d | %.1f%% |\n", model.InvalidTypeDesc[t], ts.Count, ts.Percent))
	}

	// 风险分级
	sb.WriteString("\n## 三、风险等级分布\n\n")
	sb.WriteString("| 等级 | 数量 |\n|------|------|\n")
	sb.WriteString(fmt.Sprintf("| 🔴 严重 (Critical) | %d |\n", stats.BySeverity[model.SeverityCritical]))
	sb.WriteString(fmt.Sprintf("| 🟡 一般 (Major) | %d |\n", stats.BySeverity[model.SeverityMajor]))
	sb.WriteString(fmt.Sprintf("| 🟢 轻微 (Minor) | %d |\n\n", stats.BySeverity[model.SeverityMinor]))

	// Top20
	sb.WriteString("## 四、Top 高风险无效指标\n\n")
	sb.WriteString("| # | 指标名 | 风险等级 | 问题描述 |\n|---|--------|----------|----------|\n")
	for i, inv := range stats.Top20Invalid {
		descs := make([]string, 0, len(inv.Issues))
		for _, issue := range inv.Issues {
			descs = append(descs, issue.Description)
		}
		sb.WriteString(fmt.Sprintf("| %d | `%s` | %s | %s |\n",
			i+1, inv.Metric.Name, inv.MaxSeverity, strings.Join(descs, "<br>")))
	}

	// Top20 违规标签
	if len(stats.Top20IllegalLabels) > 0 {
		sb.WriteString("\n## 五、Top 违规标签维度\n\n")
		sb.WriteString("| # | 标签 Key | 出现次数 | 涉及指标示例 |\n|---|----------|----------|----------|\n")
		for i, ls := range stats.Top20IllegalLabels {
			sb.WriteString(fmt.Sprintf("| %d | `%s` | %d | %s |\n",
				i+1, ls.LabelKey, ls.Count, strings.Join(ls.Examples, ", ")))
		}
	}

	// AI 分析（结构化）
	if ai.OverallScore > 0 || len(ai.MainPatterns) > 0 {
		sb.WriteString("\n## 六、AI 智能治理分析\n\n")
		sb.WriteString(fmt.Sprintf("> Provider: **%s** | Model: **%s**\n\n", ai.Provider, ai.Model))

		if ai.OverallScore > 0 {
			sb.WriteString(fmt.Sprintf("### 监控健康评分：%d / 100（%s）\n\n", ai.OverallScore, ai.HealthLevel))
			if ai.HealthSummary != "" {
				sb.WriteString(fmt.Sprintf("> %s\n\n", ai.HealthSummary))
			}
		}

		if len(ai.ScoreDeductions) > 0 {
			sb.WriteString("**评分扣减明细：**\n\n")
			sb.WriteString("| 扣分原因 | 分值 | 说明 |\n|----------|------|------|\n")
			for _, d := range ai.ScoreDeductions {
				sb.WriteString(fmt.Sprintf("| %s | %+d | %s |\n", d.Reason, d.Points, d.Detail))
			}
			sb.WriteString("\n")
		}

		if len(ai.MainPatterns) > 0 {
			sb.WriteString("### 主要问题模式\n\n")
			sb.WriteString("| # | 模式 | 风险 | 根因 | 涉及指标 |\n|---|------|------|------|----------|\n")
			for i, p := range ai.MainPatterns {
				sb.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %s |\n",
					i+1, p.Name, p.Severity, p.RootCause,
					strings.Join(p.AffectedMetrics, ", ")))
			}
			sb.WriteString("\n")
		}

		if len(ai.Suggestions) > 0 {
			sb.WriteString("### 优化建议\n\n")
			sb.WriteString("| 优先级 | 操作类型 | 操作 | 执行时机 | 涉及指标 | 详细步骤 |\n|--------|----------|------|---------|---------|----------|\n")
			for _, s := range ai.Suggestions {
				typeEmoji := actionTypeEmoji(s.ActionType)
				sb.WriteString(fmt.Sprintf("| P%d | %s | %s | %s | %s | %s |\n",
					s.Priority, typeEmoji, s.Action, s.Timeline,
					strings.Join(s.TargetMetrics, ", "), s.Detail))
			}
			sb.WriteString("\n")
		}

		if len(ai.RelabelRules) > 0 {
			sb.WriteString("### Relabel 规则建议\n\n")
			for _, r := range ai.RelabelRules {
				sb.WriteString(fmt.Sprintf("**%s**\n\n", r.Description))
				config := strings.ReplaceAll(r.Config, "\\n", "\n")
				sb.WriteString("```yaml\n")
				sb.WriteString(config)
				sb.WriteString("\n```\n\n")
			}
		}
	} else if ai.RawPhase1 != "" || ai.RawPhase2 != "" {
		// 解析失败兜底
		sb.WriteString("\n## 六、AI 智能治理报告\n\n")
		sb.WriteString(fmt.Sprintf("> Provider: **%s** | Model: **%s**\n\n", ai.Provider, ai.Model))
		if ai.RawPhase1 != "" {
			sb.WriteString("### 根因分析\n\n")
			sb.WriteString(ai.RawPhase1)
			sb.WriteString("\n\n")
		}
		if ai.RawPhase2 != "" {
			sb.WriteString("### 治理建议\n\n")
			sb.WriteString(ai.RawPhase2)
		}
	}

	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("write markdown report: %w", err)
	}
	fmt.Printf("  Markdown 报表已输出: %s\n", path)
	return nil
}
