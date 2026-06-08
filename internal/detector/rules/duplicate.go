package rules

import (
	"fmt"
	"sort"
	"strings"

	"github.com/memo/prometheus-analyzer/internal/model"
)

// DuplicateRule 检测重复指标（相同指标名 + 相同 Label Key 集合出现多次）
type DuplicateRule struct{}

func (r *DuplicateRule) Name() string { return "duplicate" }

func (r *DuplicateRule) Check(metrics []model.Metric) []model.InvalidMetric {
	// key: metricName + sorted label key=value 完整组合
	type entry struct {
		count int
	}
	seen := make(map[string]*entry)

	for _, m := range metrics {
		k := fingerprintFull(m)
		if e, ok := seen[k]; ok {
			e.count++
		} else {
			seen[k] = &entry{count: 1}
		}
	}

	var result []model.InvalidMetric
	for _, m := range metrics {
		k := fingerprintFull(m)
		if seen[k].count > 1 {
			result = append(result, model.InvalidMetric{
				Metric: m,
				Issues: []model.DetectedIssue{{
					Type:        model.TypeDuplicate,
					Severity:    model.SeverityMajor,
					Description: fmt.Sprintf("完全相同的指标（名称+标签完全一致）重复上报 %d 次", seen[k].count),
				}},
				MaxSeverity: model.SeverityMajor,
			})
		}
	}
	return result
}

// fingerprintFull 生成 指标名 + 完整 label key=value 组合的唯一键
func fingerprintFull(m model.Metric) string {
	pairs := make([]string, 0, len(m.Labels))
	for _, l := range m.Labels {
		pairs = append(pairs, l.Key+"="+l.Value)
	}
	sort.Strings(pairs)
	return m.Name + "{" + strings.Join(pairs, ",") + "}"
}

