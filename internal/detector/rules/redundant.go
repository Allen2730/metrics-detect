package rules

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/memo/prometheus-analyzer/internal/model"
)

// testEnvValues 属于测试/调试环境的 env 标签值
var testEnvValues = map[string]struct{}{
	"test": {}, "testing": {}, "dev": {}, "development": {},
	"debug": {}, "local": {}, "ci": {}, "sandbox": {},
}

// CandidateRedundantRule 冗余无意义指标检测，全量收集所有命中证据后综合定级。
//
// 检测链路（两阶段）：
//
//	Phase 1 静态分析（文件 + Prometheus 模式均执行）
//	  Signal A：环境/用途标签（env=test/dev/debug 等）    → 置信度 MEDIUM
//	  Signal B：名称启发式（test_/debug_/tmp_ 等前缀）     → 置信度 LOW
//	  Signal C：结构特征（完全无标签，无法聚合/过滤）       → 置信度 LOW
//
//	Phase 2 动态分析（仅 Prometheus 模式，对应字段非 nil 时执行）
//	  Signal D：消费证据（复用 RuleReferences）
//	            未被任何规则引用 → 置信度补强 MEDIUM booster
//	            已被规则引用     → 输出反证提示，降低误报
//	  Signal E：历史行为（来自 /api/v1/query 批量查询）
//	            IsStale=true       → 近 5 分钟无新样本，序列已 stale    → 置信度 HIGH
//	            IsAlwaysZero=true  → 近 7 天最大值为 0，长期无业务意义   → 置信度 HIGH
//	            IsStatic=true      → 近 7 天值无变化（非零恒定）          → 置信度 HIGH
//
//	综合定级：
//	  任意 HIGH 信号                              → 整体置信度 HIGH
//	  环境标签 MEDIUM 单独命中                    → 整体置信度 MEDIUM
//	  消费 MEDIUM booster + 任意 LOW 信号叠加     → 整体置信度 MEDIUM
//	  ≥2 个 LOW 信号                             → 整体置信度 MEDIUM
//	  仅 1 个 LOW 信号                           → 整体置信度 LOW（需人工复核）
type CandidateRedundantRule struct {
	Patterns []string // 原始正则字符串，保留用于描述输出
	compiled []*regexp.Regexp

	// 以下字段由 Prometheus 模式注入，nil 表示文件模式（跳过动态分析）
	RuleReferences map[string][]string            // 来自 /api/v1/rules
	ActivityInfo   map[string]model.MetricActivityInfo // 来自 /api/v1/query
}

func NewCandidateRedundantRule(patterns []string) *CandidateRedundantRule {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		if re, err := regexp.Compile(p); err == nil {
			compiled = append(compiled, re)
		}
	}
	return &CandidateRedundantRule{Patterns: patterns, compiled: compiled}
}

func (r *CandidateRedundantRule) Name() string { return "candidate_redundant" }

type redundancyEvidence struct {
	signal     string
	confidence model.Confidence
	desc       string
}

func (r *CandidateRedundantRule) Check(metrics []model.Metric) []model.InvalidMetric {
	var result []model.InvalidMetric
	for _, m := range metrics {
		if inv, ok := r.checkOne(m); ok {
			result = append(result, inv)
		}
	}
	return result
}

func (r *CandidateRedundantRule) checkOne(m model.Metric) (model.InvalidMetric, bool) {
	var evidences []redundancyEvidence

	// ── Phase 1A：环境/用途标签 ───────────────────────────────────
	if ev := r.checkEnvLabel(m); ev != nil {
		evidences = append(evidences, *ev)
	}

	// ── Phase 1B：名称启发式 ──────────────────────────────────────
	if ev := r.checkNamePattern(m); ev != nil {
		evidences = append(evidences, *ev)
	}

	// ── Phase 1C：结构特征（无标签） ──────────────────────────────
	if len(m.Labels) == 0 && m.Name != "" {
		evidences = append(evidences, redundancyEvidence{
			signal:     "no_labels",
			confidence: model.ConfidenceLow,
			desc:       "指标无任何标签，无法按维度聚合或过滤，实用价值存疑（弱信号，需人工复核）",
		})
	}

	// ── Phase 2D：消费证据（复用 RuleReferences） ─────────────────
	if r.RuleReferences != nil {
		if refs, cited := r.RuleReferences[m.Name]; cited {
			evidences = append(evidences, redundancyEvidence{
				signal:     "rule_cited",
				confidence: model.ConfidenceLow,
				desc: fmt.Sprintf(
					"仍被 %d 条 Prometheus 规则引用（%s），存在活跃消费者，冗余可能性降低",
					len(refs), strings.Join(firstNStr(refs, 3), ", "),
				),
			})
		} else {
			evidences = append(evidences, redundancyEvidence{
				signal:     "no_rule_reference",
				confidence: model.ConfidenceMedium,
				desc:       "未被任何 Prometheus 告警/记录规则引用（不排除 Grafana 等外部消费）",
			})
		}
	}

	// ── Phase 2E：历史行为（/api/v1/query 批量结果） ──────────────
	if r.ActivityInfo != nil {
		if info, ok := r.ActivityInfo[m.Name]; ok {
			if info.IsStale {
				evidences = append(evidences, redundancyEvidence{
					signal:     "stale",
					confidence: model.ConfidenceHigh,
					desc:       "近 5 分钟无新样本，序列已 stale（历史行为证据）",
				})
			}
			if info.IsAlwaysZero {
				evidences = append(evidences, redundancyEvidence{
					signal:     "always_zero",
					confidence: model.ConfidenceHigh,
					desc:       "近 7 天最大值为 0，长期无业务意义（历史行为证据）",
				})
			}
			if info.IsStatic {
				evidences = append(evidences, redundancyEvidence{
					signal:     "static_value",
					confidence: model.ConfidenceHigh,
					desc:       "近 7 天值无任何变化（非零恒定值），不反映任何业务动态（历史行为证据）",
				})
			}
		}
	}

	if len(evidences) == 0 {
		return model.InvalidMetric{}, false
	}

	// 被规则引用是反证；如果除反证外没有任何正向信号，不标记
	hasPositiveSignal := false
	for _, ev := range evidences {
		if ev.signal != "rule_cited" {
			hasPositiveSignal = true
			break
		}
	}
	if !hasPositiveSignal {
		return model.InvalidMetric{}, false
	}

	finalConf := synthesizeRedundantConfidence(evidences)

	issues := make([]model.DetectedIssue, 0, len(evidences))
	for _, ev := range evidences {
		issues = append(issues, model.DetectedIssue{
			Type:        model.TypeCandidateRedundant,
			Severity:    model.SeverityMinor,
			Confidence:  ev.confidence,
			Description: ev.desc,
		})
	}
	if len(issues) > 0 {
		issues[0].Confidence = finalConf
	}

	return model.InvalidMetric{
		Metric:      m,
		Issues:      issues,
		MaxSeverity: model.SeverityMinor,
	}, true
}

func (r *CandidateRedundantRule) checkEnvLabel(m model.Metric) *redundancyEvidence {
	for _, l := range m.Labels {
		if l.Key != "env" && l.Key != "environment" {
			continue
		}
		if _, isTest := testEnvValues[strings.ToLower(l.Value)]; isTest {
			return &redundancyEvidence{
				signal:     "test_env",
				confidence: model.ConfidenceMedium,
				desc:       fmt.Sprintf("环境标签 %s=%q 表明该指标属于非生产环境（环境证据）", l.Key, l.Value),
			}
		}
	}
	return nil
}

func (r *CandidateRedundantRule) checkNamePattern(m model.Metric) *redundancyEvidence {
	for i, re := range r.compiled {
		if re.MatchString(m.Name) {
			pattern := re.String()
			if i < len(r.Patterns) {
				pattern = r.Patterns[i]
			}
			return &redundancyEvidence{
				signal:     "name_pattern",
				confidence: model.ConfidenceLow,
				desc:       fmt.Sprintf("名称匹配疑似冗余模式 %q（弱信号，仅名称启发，需人工复核）", pattern),
			}
		}
	}
	return nil
}

// synthesizeRedundantConfidence 综合所有证据确定最终置信度
func synthesizeRedundantConfidence(evidences []redundancyEvidence) model.Confidence {
	hasHigh := false
	hasMediumDirect := false  // 环境标签（直接 MEDIUM，单独成立）
	hasMediumBooster := false // 消费证据（MEDIUM booster，需叠加）
	lowCount := 0

	for _, ev := range evidences {
		if ev.signal == "rule_cited" {
			continue // 反证，不计入正向强度
		}
		switch ev.confidence {
		case model.ConfidenceHigh:
			hasHigh = true
		case model.ConfidenceMedium:
			if ev.signal == "no_rule_reference" {
				hasMediumBooster = true
			} else {
				hasMediumDirect = true
			}
		case model.ConfidenceLow:
			lowCount++
		}
	}

	switch {
	case hasHigh:
		return model.ConfidenceHigh
	case hasMediumDirect:
		return model.ConfidenceMedium
	case hasMediumBooster && lowCount > 0:
		return model.ConfidenceMedium
	case lowCount >= 2:
		return model.ConfidenceMedium
	default:
		return model.ConfidenceLow
	}
}

func firstNStr(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
