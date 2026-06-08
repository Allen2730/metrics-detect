package reporter

import (
	"fmt"
	"strings"

	"github.com/memo/prometheus-analyzer/internal/model"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorYellow = "\033[33m"
	colorGreen  = "\033[32m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
)

// ConsoleReporter 控制台彩色输出
type ConsoleReporter struct{}

func NewConsoleReporter() *ConsoleReporter { return &ConsoleReporter{} }

func (r *ConsoleReporter) Write(stats model.StatisticsReport, ai model.AIAnalysisResult) error {
	printBanner()
	printSummary(stats)
	printByType(stats)
	printBySeverity(stats)
	printTop20(stats)
	if stats.Top20IllegalLabels != nil {
		printTop20Labels(stats)
	}
	if ai.Provider != "" {
		printAIResult(ai)
	}
	return nil
}

func printBanner() {
	fmt.Printf("\n%s%s========================================%s\n", colorBold, colorCyan, colorReset)
	fmt.Printf("%s%s  Prometheus 无效指标智能分析报告%s\n", colorBold, colorCyan, colorReset)
	fmt.Printf("%s%s========================================%s\n\n", colorBold, colorCyan, colorReset)
}

func printSummary(stats model.StatisticsReport) {
	fmt.Printf("%s【总览】%s\n", colorBold, colorReset)
	fmt.Printf("  %-20s %d\n", "指标总数:", stats.TotalMetrics)
	fmt.Printf("  %-20s %s%d%s\n", "有效指标:", colorGreen, stats.ValidMetrics, colorReset)
	fmt.Printf("  %-20s %s%d%s (%.1f%%)\n\n", "无效指标:",
		colorRed, stats.InvalidMetrics, colorReset, stats.InvalidPercent)
}

func printByType(stats model.StatisticsReport) {
	fmt.Printf("%s【无效指标分类统计】%s\n", colorBold, colorReset)
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
		bar := progressBar(ts.Percent, 20)
		fmt.Printf("  %-18s %s %3d条 (%.1f%%)\n",
			model.InvalidTypeDesc[t]+":", bar, ts.Count, ts.Percent)
	}
	fmt.Println()
}

func printBySeverity(stats model.StatisticsReport) {
	fmt.Printf("%s【风险等级分布】%s\n", colorBold, colorReset)
	fmt.Printf("  %s严重 (Critical)%s: %d 条\n", colorRed, colorReset, stats.BySeverity[model.SeverityCritical])
	fmt.Printf("  %s一般 (Major)%s:   %d 条\n", colorYellow, colorReset, stats.BySeverity[model.SeverityMajor])
	fmt.Printf("  %s轻微 (Minor)%s:   %d 条\n\n", colorGreen, colorReset, stats.BySeverity[model.SeverityMinor])
}

func printTop20(stats model.StatisticsReport) {
	fmt.Printf("%s【Top %d 高风险无效指标】%s\n", colorBold, len(stats.Top20Invalid), colorReset)
	for i, inv := range stats.Top20Invalid {
		sevColor := severityColor(inv.MaxSeverity)
		fmt.Printf("  %2d. %s[%s]%s %s\n", i+1,
			sevColor, inv.MaxSeverity, colorReset, inv.Metric.Name)
		for _, issue := range inv.Issues {
			confMark := confidenceMark(issue.Confidence)
			fmt.Printf("      ↳ %s%s\n", confMark, issue.Description)
		}
	}
	fmt.Println()
}

func confidenceMark(c model.Confidence) string {
	switch c {
	case model.ConfidenceHigh:
		return ""
	case model.ConfidenceMedium:
		return "[置信度:中] "
	case model.ConfidenceLow:
		return "[置信度:低] "
	default:
		return ""
	}
}

func printTop20Labels(stats model.StatisticsReport) {
	if len(stats.Top20IllegalLabels) == 0 {
		return
	}
	fmt.Printf("%s【Top %d 违规标签】%s\n", colorBold, len(stats.Top20IllegalLabels), colorReset)
	for i, ls := range stats.Top20IllegalLabels {
		fmt.Printf("  %2d. %-30s 出现 %d 次\n", i+1, ls.LabelKey, ls.Count)
	}
	fmt.Println()
}

func printAIResult(ai model.AIAnalysisResult) {
	fmt.Printf("%s【AI 智能分析（%s / %s）】%s\n\n", colorBold, ai.Provider, ai.Model, colorReset)

	// 健康评分
	if ai.OverallScore > 0 {
		scoreColor := scoreColor(ai.OverallScore)
		fmt.Printf("  %s健康评分：%d / 100%s  状态：%s\n",
			scoreColor, ai.OverallScore, colorReset, ai.HealthLevel)
		if ai.HealthSummary != "" {
			fmt.Printf("  %s\n", ai.HealthSummary)
		}
		fmt.Println()
	}

	// 主要问题模式
	if len(ai.MainPatterns) > 0 {
		fmt.Printf("%s  主要问题模式：%s\n", colorBold, colorReset)
		for i, p := range ai.MainPatterns {
			sevColor := patternSeverityColor(p.Severity)
			fmt.Printf("  %d. %s[%s]%s %s\n", i+1, sevColor, p.Severity, colorReset, p.Name)
			fmt.Printf("     根因：%s\n", p.RootCause)
			if len(p.AffectedMetrics) > 0 {
				fmt.Printf("     涉及：%s\n", strings.Join(p.AffectedMetrics, ", "))
			}
		}
		fmt.Println()
	}

	// 扣分明细
	if len(ai.ScoreDeductions) > 0 {
		fmt.Printf("%s  评分扣减明细：%s\n", colorBold, colorReset)
		for _, d := range ai.ScoreDeductions {
			fmt.Printf("  %s%+d%s  %s — %s\n",
				colorRed, d.Points, colorReset, d.Reason, d.Detail)
		}
		fmt.Println()
	}

	// 优化建议
	if len(ai.Suggestions) > 0 {
		fmt.Printf("%s  优化建议（按优先级）：%s\n", colorBold, colorReset)
		for _, s := range ai.Suggestions {
			tag, tagColor := actionTypeTag(s.ActionType)
			fmt.Printf("  P%d %s%s%s %s\n",
				s.Priority, tagColor, tag, colorReset, s.Action)
			if len(s.TargetMetrics) > 0 {
				fmt.Printf("     指标：%s\n", strings.Join(s.TargetMetrics, ", "))
			}
			fmt.Printf("     %s\n", s.Detail)
			if s.Timeline != "" {
				fmt.Printf("     时机：%s\n", s.Timeline)
			}
		}
		fmt.Println()
	}

	// Relabel 规则
	if len(ai.RelabelRules) > 0 {
		fmt.Printf("%s  Relabel 规则建议：%s\n", colorBold, colorReset)
		for _, r := range ai.RelabelRules {
			fmt.Printf("  # %s\n", r.Description)
			for _, line := range strings.Split(r.Config, "\\n") {
				fmt.Printf("  %s\n", line)
			}
			fmt.Println()
		}
	}

	// 解析失败时的兜底
	if ai.OverallScore == 0 && len(ai.MainPatterns) == 0 {
		if ai.RawPhase1 != "" {
			fmt.Printf("%s  根因分析：%s\n%s\n", colorBold, colorReset, ai.RawPhase1)
		}
		if ai.RawPhase2 != "" {
			fmt.Printf("%s  治理建议：%s\n%s\n", colorBold, colorReset, ai.RawPhase2)
		}
	}
}

func scoreColor(score int) string {
	switch {
	case score >= 80:
		return colorGreen
	case score >= 60:
		return colorYellow
	default:
		return colorRed
	}
}

func actionTypeTag(at model.ActionType) (tag, color string) {
	switch at {
	case model.ActionTypeDirect:
		return "[直接执行]", colorRed
	case model.ActionTypeConfirmFirst:
		return "[先确认]  ", colorYellow
	case model.ActionTypeInvestigate:
		return "[仅调查]  ", colorCyan
	default:
		return "[待确认]  ", colorYellow
	}
}

func patternSeverityColor(severity string) string {
	switch severity {
	case "critical":
		return colorRed
	case "major":
		return colorYellow
	default:
		return colorGreen
	}
}

func progressBar(pct float64, width int) string {
	filled := int(pct / 100.0 * float64(width))
	if filled > width {
		filled = width
	}
	bar := ""
	for i := 0; i < width; i++ {
		if i < filled {
			bar += "█"
		} else {
			bar += "░"
		}
	}
	return bar
}

func severityColor(s model.Severity) string {
	switch s {
	case model.SeverityCritical:
		return colorRed
	case model.SeverityMajor:
		return colorYellow
	default:
		return colorGreen
	}
}
