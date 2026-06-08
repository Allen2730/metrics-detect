package rules

import "github.com/memo/prometheus-analyzer/internal/model"

// Rule 检测规则接口
type Rule interface {
	Name() string
	Check(metrics []model.Metric) []model.InvalidMetric
}
