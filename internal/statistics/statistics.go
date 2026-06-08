package statistics

import (
	"sort"

	"github.com/memo/prometheus-analyzer/internal/model"
)

const top20Limit = 20

// Calculate 从全量指标和无效指标列表生成统计报告
func Calculate(metrics []model.Metric, invalids []model.InvalidMetric) model.StatisticsReport {
	total := len(metrics)
	invalidCount := countUnique(invalids)

	valid := total - invalidCount
	if valid < 0 {
		valid = 0
	}

	invalidPct := 0.0
	if total > 0 {
		invalidPct = float64(invalidCount) / float64(total) * 100
	}

	byType := calcByType(invalids, total)
	bySev := calcBySeverity(invalids)
	top20 := calcTop20Invalid(invalids)
	top20Labels := calcTop20IllegalLabels(invalids)

	return model.StatisticsReport{
		TotalMetrics:       total,
		ValidMetrics:       valid,
		InvalidMetrics:     invalidCount,
		InvalidPercent:     invalidPct,
		ByType:             byType,
		BySeverity:         bySev,
		Top20Invalid:       top20,
		Top20IllegalLabels: top20Labels,
	}
}

// countUnique 去重计数（同一 Metric 可能被多个规则命中，只计一次）
func countUnique(invalids []model.InvalidMetric) int {
	seen := make(map[string]struct{})
	for _, inv := range invalids {
		key := inv.Metric.Name + inv.Metric.RawLine
		seen[key] = struct{}{}
	}
	return len(seen)
}

func calcByType(invalids []model.InvalidMetric, total int) map[model.InvalidType]model.TypeStats {
	counts := make(map[model.InvalidType]int)
	for _, inv := range invalids {
		typeSet := make(map[model.InvalidType]struct{})
		for _, issue := range inv.Issues {
			typeSet[issue.Type] = struct{}{}
		}
		for t := range typeSet {
			counts[t]++
		}
	}
	result := make(map[model.InvalidType]model.TypeStats, len(counts))
	for t, c := range counts {
		pct := 0.0
		if total > 0 {
			pct = float64(c) / float64(total) * 100
		}
		result[t] = model.TypeStats{Count: c, Percent: pct}
	}
	return result
}

func calcBySeverity(invalids []model.InvalidMetric) map[model.Severity]int {
	result := map[model.Severity]int{
		model.SeverityCritical: 0,
		model.SeverityMajor:    0,
		model.SeverityMinor:    0,
	}
	for _, inv := range invalids {
		result[inv.MaxSeverity]++
	}
	return result
}

func calcTop20Invalid(invalids []model.InvalidMetric) []model.InvalidMetric {
	// 按指标名去重，保留问题最多（issues数量最多）的那条
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
			(inv.MaxSeverity == existing.MaxSeverity && len(inv.Issues) > len(existing.Issues)) {
			cp := *inv
			byName[inv.Metric.Name] = &cp
		}
	}

	deduped := make([]model.InvalidMetric, 0, len(byName))
	for _, v := range byName {
		deduped = append(deduped, *v)
	}
	sort.Slice(deduped, func(i, j int) bool {
		si := model.SeverityOrder[deduped[i].MaxSeverity]
		sj := model.SeverityOrder[deduped[j].MaxSeverity]
		if si != sj {
			return si > sj
		}
		return len(deduped[i].Issues) > len(deduped[j].Issues)
	})
	if len(deduped) > top20Limit {
		return deduped[:top20Limit]
	}
	return deduped
}

func calcTop20IllegalLabels(invalids []model.InvalidMetric) []model.LabelStat {
	labelCount := make(map[string]int)
	labelExamples := make(map[string][]string)

	for _, inv := range invalids {
		for _, issue := range inv.Issues {
			if issue.Type != model.TypeIllegalLabel {
				continue
			}
			for _, l := range inv.Metric.Labels {
				if isIllegalKey(l.Key) {
					labelCount[l.Key]++
					if len(labelExamples[l.Key]) < 3 {
						labelExamples[l.Key] = append(labelExamples[l.Key], inv.Metric.Name)
					}
				}
			}
		}
	}

	stats := make([]model.LabelStat, 0, len(labelCount))
	for k, c := range labelCount {
		stats = append(stats, model.LabelStat{
			LabelKey: k,
			Count:    c,
			Examples: labelExamples[k],
		})
	}
	sort.Slice(stats, func(i, j int) bool {
		return stats[i].Count > stats[j].Count
	})
	if len(stats) > top20Limit {
		return stats[:top20Limit]
	}
	return stats
}

func isIllegalKey(key string) bool {
	if key == "" {
		return false
	}
	for i, ch := range key {
		if i == 0 {
			if !isLetter(ch) && ch != '_' {
				return true
			}
		} else {
			if !isLetter(ch) && !isDigit(ch) && ch != '_' {
				return true
			}
		}
	}
	return false
}

func isLetter(ch rune) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}
func isDigit(ch rune) bool { return ch >= '0' && ch <= '9' }
