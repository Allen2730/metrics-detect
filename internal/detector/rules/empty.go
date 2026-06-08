package rules

import (
	"math"

	"github.com/memo/prometheus-analyzer/internal/model"
)

// EmptyRule 检测空名称/空标签键/空标签值/NaN值指标
type EmptyRule struct{}

func (r *EmptyRule) Name() string { return "empty" }

func (r *EmptyRule) Check(metrics []model.Metric) []model.InvalidMetric {
	var result []model.InvalidMetric
	for _, m := range metrics {
		var issues []model.DetectedIssue

		if m.Name == "" {
			issues = append(issues, model.DetectedIssue{
				Type:        model.TypeEmpty,
				Severity:    model.SeverityCritical,
				Description: "指标名称为空",
			})
		}

		emptyKeyCount := 0
		emptyValCount := 0
		for _, l := range m.Labels {
			if l.Key == "" {
				emptyKeyCount++
			}
			if l.Value == "" {
				emptyValCount++
			}
		}
		if emptyKeyCount > 0 {
			issues = append(issues, model.DetectedIssue{
				Type:        model.TypeEmpty,
				Severity:    model.SeverityCritical,
				Description: "存在空 Label Key",
			})
		}
		if emptyValCount > 0 && emptyValCount == len(m.Labels) {
			// 全部标签 value 为空才认定
			issues = append(issues, model.DetectedIssue{
				Type:        model.TypeEmpty,
				Severity:    model.SeverityCritical,
				Description: "所有 Label Value 均为空字符串",
			})
		}

		if math.IsNaN(m.Value) {
			issues = append(issues, model.DetectedIssue{
				Type:        model.TypeEmpty,
				Severity:    model.SeverityCritical,
				Description: "指标值为 NaN",
			})
		}

		if len(issues) > 0 {
			result = append(result, model.InvalidMetric{
				Metric:      m,
				Issues:      issues,
				MaxSeverity: model.SeverityCritical,
			})
		}
	}
	return result
}
