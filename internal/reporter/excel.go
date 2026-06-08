package reporter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/memo/prometheus-analyzer/internal/model"
	"github.com/xuri/excelize/v2"
)

// ExcelReporter 输出 Excel 报表
type ExcelReporter struct {
	OutputDir string
}

func NewExcelReporter(dir string) *ExcelReporter {
	return &ExcelReporter{OutputDir: dir}
}

func (r *ExcelReporter) Write(stats model.StatisticsReport, ai model.AIAnalysisResult) error {
	if err := os.MkdirAll(r.OutputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	f := excelize.NewFile()
	defer f.Close()

	r.writeSummarySheet(f, stats)
	r.writeInvalidListSheet(f, stats)
	r.writeIllegalLabelsSheet(f, stats)
	if ai.Provider != "" {
		r.writeAIReportSheet(f, ai)
	}

	// 删除默认 Sheet1
	f.DeleteSheet("Sheet1")

	filename := fmt.Sprintf("report_%s.xlsx", time.Now().Format("20060102_150405"))
	path := filepath.Join(r.OutputDir, filename)
	if err := f.SaveAs(path); err != nil {
		return fmt.Errorf("save excel: %w", err)
	}
	fmt.Printf("  Excel 报表已输出: %s\n", path)
	return nil
}

func (r *ExcelReporter) writeSummarySheet(f *excelize.File, stats model.StatisticsReport) {
	sheet := "总览"
	f.NewSheet(sheet)

	headers := []string{"指标", "数值"}
	setRow(f, sheet, 1, headers, true)
	rows := [][]string{
		{"指标总数", fmt.Sprintf("%d", stats.TotalMetrics)},
		{"有效指标", fmt.Sprintf("%d", stats.ValidMetrics)},
		{"无效指标", fmt.Sprintf("%d", stats.InvalidMetrics)},
		{"无效占比", fmt.Sprintf("%.1f%%", stats.InvalidPercent)},
		{"严重(Critical)", fmt.Sprintf("%d", stats.BySeverity[model.SeverityCritical])},
		{"一般(Major)", fmt.Sprintf("%d", stats.BySeverity[model.SeverityMajor])},
		{"轻微(Minor)", fmt.Sprintf("%d", stats.BySeverity[model.SeverityMinor])},
	}
	for i, row := range rows {
		setRow(f, sheet, i+2, row, false)
	}
	f.SetColWidth(sheet, "A", "B", 20)
}

func (r *ExcelReporter) writeInvalidListSheet(f *excelize.File, stats model.StatisticsReport) {
	sheet := "无效指标列表"
	f.NewSheet(sheet)

	headers := []string{"序号", "指标名", "风险等级", "问题类型", "问题描述"}
	setRow(f, sheet, 1, headers, true)

	for i, inv := range stats.Top20Invalid {
		types := make([]string, 0)
		descs := make([]string, 0)
		seen := make(map[model.InvalidType]struct{})
		for _, issue := range inv.Issues {
			if _, ok := seen[issue.Type]; !ok {
				types = append(types, string(issue.Type))
				seen[issue.Type] = struct{}{}
			}
			descs = append(descs, issue.Description)
		}
		setRow(f, sheet, i+2, []string{
			fmt.Sprintf("%d", i+1),
			inv.Metric.Name,
			string(inv.MaxSeverity),
			strings.Join(types, ", "),
			strings.Join(descs, "; "),
		}, false)
	}
	f.SetColWidth(sheet, "A", "A", 6)
	f.SetColWidth(sheet, "B", "B", 45)
	f.SetColWidth(sheet, "C", "C", 12)
	f.SetColWidth(sheet, "D", "D", 25)
	f.SetColWidth(sheet, "E", "E", 60)
}

func (r *ExcelReporter) writeIllegalLabelsSheet(f *excelize.File, stats model.StatisticsReport) {
	if len(stats.Top20IllegalLabels) == 0 {
		return
	}
	sheet := "违规标签"
	f.NewSheet(sheet)

	headers := []string{"序号", "标签 Key", "出现次数", "涉及指标示例"}
	setRow(f, sheet, 1, headers, true)

	for i, ls := range stats.Top20IllegalLabels {
		setRow(f, sheet, i+2, []string{
			fmt.Sprintf("%d", i+1),
			ls.LabelKey,
			fmt.Sprintf("%d", ls.Count),
			strings.Join(ls.Examples, ", "),
		}, false)
	}
	f.SetColWidth(sheet, "A", "A", 6)
	f.SetColWidth(sheet, "B", "B", 35)
	f.SetColWidth(sheet, "C", "C", 12)
	f.SetColWidth(sheet, "D", "D", 50)
}

func (r *ExcelReporter) writeAIReportSheet(f *excelize.File, ai model.AIAnalysisResult) {
	sheet := "AI治理报告"
	f.NewSheet(sheet)

	row := 1
	setRow(f, sheet, row, []string{
		fmt.Sprintf("Provider: %s | Model: %s | 健康评分: %d/100（%s）",
			ai.Provider, ai.Model, ai.OverallScore, ai.HealthLevel),
	}, true)
	row++

	if ai.HealthSummary != "" {
		f.SetCellValue(sheet, fmt.Sprintf("A%d", row), ai.HealthSummary)
		row += 2
	}

	// 评分扣减
	if len(ai.ScoreDeductions) > 0 {
		setRow(f, sheet, row, []string{"扣分原因", "分值", "说明"}, true)
		row++
		for _, d := range ai.ScoreDeductions {
			setRow(f, sheet, row, []string{d.Reason, fmt.Sprintf("%+d", d.Points), d.Detail}, false)
			row++
		}
		row++
	}

	// 主要问题模式
	if len(ai.MainPatterns) > 0 {
		setRow(f, sheet, row, []string{"问题模式", "风险", "根因", "涉及指标"}, true)
		row++
		for _, p := range ai.MainPatterns {
			setRow(f, sheet, row, []string{
				p.Name, p.Severity, p.RootCause,
				strings.Join(p.AffectedMetrics, ", "),
			}, false)
			row++
		}
		row++
	}

	// 优化建议
	if len(ai.Suggestions) > 0 {
		setRow(f, sheet, row, []string{"优先级", "操作", "执行时机", "涉及指标", "详细步骤"}, true)
		row++
		for _, s := range ai.Suggestions {
			setRow(f, sheet, row, []string{
				fmt.Sprintf("P%d", s.Priority),
				s.Action, s.Timeline,
				strings.Join(s.TargetMetrics, ", "),
				s.Detail,
			}, false)
			row++
		}
		row++
	}

	// Relabel 规则
	if len(ai.RelabelRules) > 0 {
		setRow(f, sheet, row, []string{"规则说明", "配置内容"}, true)
		row++
		for _, rule := range ai.RelabelRules {
			config := strings.ReplaceAll(rule.Config, "\\n", "\n")
			setRow(f, sheet, row, []string{rule.Description, config}, false)
			row++
		}
	}

	f.SetColWidth(sheet, "A", "A", 35)
	f.SetColWidth(sheet, "B", "B", 15)
	f.SetColWidth(sheet, "C", "C", 55)
	f.SetColWidth(sheet, "D", "D", 35)
	f.SetColWidth(sheet, "E", "E", 60)
}

func setRow(f *excelize.File, sheet string, row int, values []string, bold bool) {
	cols := []string{"A", "B", "C", "D", "E", "F"}
	style := 0
	if bold {
		s, _ := f.NewStyle(&excelize.Style{
			Font: &excelize.Font{Bold: true},
			Fill: excelize.Fill{Type: "pattern", Color: []string{"#D9E1F2"}, Pattern: 1},
		})
		style = s
	}
	for i, v := range values {
		if i >= len(cols) {
			break
		}
		cell := fmt.Sprintf("%s%d", cols[i], row)
		f.SetCellValue(sheet, cell, v)
		if bold {
			f.SetCellStyle(sheet, cell, cell, style)
		}
	}
}
