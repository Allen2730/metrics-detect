package rules

import (
	"fmt"
	"sort"
	"strings"

	"github.com/memo/prometheus-analyzer/internal/model"
)

// HighCardinalityRule 检测高基数指标，按超出阈值的倍数做三级分级：
//
//	1× ~ 10×  → Minor   轻微超标，建议观察
//	10× ~ 100× → Major   显著超标，需纳入整改计划
//	100×+      → Critical 极高基数，存在 OOM 风险，立即处理
type HighCardinalityRule struct {
	Threshold int
}

func (r *HighCardinalityRule) Name() string { return "high_cardinality" }

func (r *HighCardinalityRule) Check(metrics []model.Metric) []model.InvalidMetric {
	type groupEntry struct {
		combinations map[string]struct{}
	}
	groups := make(map[string]*groupEntry)

	for _, m := range metrics {
		if _, ok := groups[m.Name]; !ok {
			groups[m.Name] = &groupEntry{combinations: make(map[string]struct{})}
		}
		groups[m.Name].combinations[fingerprintValues(m)] = struct{}{}
	}

	var result []model.InvalidMetric
	for _, m := range metrics {
		count := len(groups[m.Name].combinations)
		if count <= r.Threshold {
			continue
		}
		sev, label := r.classify(count)
		result = append(result, model.InvalidMetric{
			Metric: m,
			Issues: []model.DetectedIssue{{
				Type:     model.TypeHighCardinality,
				Severity: sev,
				Description: fmt.Sprintf(
					"指标 '%s' Label 组合数为 %d（阈值 %d，超出 %.1f 倍）%s",
					m.Name, count, r.Threshold,
					float64(count)/float64(r.Threshold),
					label,
				),
			}},
			MaxSeverity: sev,
		})
	}
	return result
}

// classify 按超出倍数返回风险等级和说明标签
func (r *HighCardinalityRule) classify(count int) (model.Severity, string) {
	ratio := count / r.Threshold
	switch {
	case ratio >= 100:
		return model.SeverityCritical, "，极高基数，存在 OOM 风险"
	case ratio >= 10:
		return model.SeverityMajor, "，显著超标，需纳入整改计划"
	default:
		return model.SeverityMinor, "，轻微超标，建议持续观察"
	}
}

// fingerprintValues 生成 label key=value 组合的指纹
func fingerprintValues(m model.Metric) string {
	pairs := make([]string, 0, len(m.Labels))
	for _, l := range m.Labels {
		pairs = append(pairs, l.Key+"="+l.Value)
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}
