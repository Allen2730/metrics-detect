package rules

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/memo/prometheus-analyzer/internal/model"
)

var validLabelKeyRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// IllegalLabelRule 检测违规标签（不合法的 Label Key）
type IllegalLabelRule struct{}

func (r *IllegalLabelRule) Name() string { return "illegal_label" }

func (r *IllegalLabelRule) Check(metrics []model.Metric) []model.InvalidMetric {
	var result []model.InvalidMetric
	for _, m := range metrics {
		var issues []model.DetectedIssue
		for _, l := range m.Labels {
			if l.Key == "" {
				continue // empty rule 已覆盖
			}
			if strings.HasPrefix(l.Key, "__") {
				issues = append(issues, model.DetectedIssue{
					Type:        model.TypeIllegalLabel,
					Severity:    model.SeverityCritical,
					Description: fmt.Sprintf("Label Key '%s' 使用了保留前缀 '__'", l.Key),
				})
				continue
			}
			if len(l.Key) > 64 {
				issues = append(issues, model.DetectedIssue{
					Type:        model.TypeIllegalLabel,
					Severity:    model.SeverityCritical,
					Description: fmt.Sprintf("Label Key '%s' 长度超过64字符", l.Key),
				})
				continue
			}
			if !validLabelKeyRe.MatchString(l.Key) {
				issues = append(issues, model.DetectedIssue{
					Type:        model.TypeIllegalLabel,
					Severity:    model.SeverityCritical,
					Description: fmt.Sprintf("Label Key '%s' 包含非法字符，不符合 [a-zA-Z_][a-zA-Z0-9_]* 规范", l.Key),
				})
			}
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
