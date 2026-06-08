package rules

import (
	"fmt"
	"strings"

	"github.com/memo/prometheus-analyzer/internal/model"
)

// CandidateDeprecatedRule 废弃指标检测，全量收集所有命中证据后综合定级。
//
// 检测链路（两阶段）：
//
//	Phase 1 静态分析（文件 + Prometheus 模式均执行）
//	  Signal A：HELP 文本含显式废弃声明            → 置信度 HIGH
//	  Signal B：携带生命周期标签                   → 置信度 HIGH
//	  Signal C：名称启发式（前缀/后缀/关键字）       → 置信度 LOW
//
//	Phase 2 动态分析（仅 Prometheus 模式，RuleReferences 非 nil 时执行）
//	  Signal D：未被任何 Prometheus 规则引用        → 置信度补强（MEDIUM booster）
//	            此信号单独不触发检测，需与 Phase 1 信号叠加
//
//	综合定级：
//	  任意 HIGH 信号                           → 整体置信度 HIGH
//	  MEDIUM booster + 任意 LOW 信号           → 整体置信度 MEDIUM
//	  ≥2 个 LOW 信号                           → 整体置信度 MEDIUM
//	  仅 1 个 LOW 信号                         → 整体置信度 LOW（需人工复核）
type CandidateDeprecatedRule struct {
	Prefixes []string
	Suffixes []string
	// RuleReferences: metricName → 引用它的规则名列表
	// nil = 文件模式（仅静态分析），非 nil = Prometheus 模式（静态 + 动态）
	RuleReferences map[string][]string
}

func (r *CandidateDeprecatedRule) Name() string { return "candidate_deprecated" }

// deprecationEvidence 单条证据
type deprecationEvidence struct {
	signal     string             // 信号标识，用于综合定级判断
	confidence model.Confidence
	desc       string
}

func (r *CandidateDeprecatedRule) Check(metrics []model.Metric) []model.InvalidMetric {
	var result []model.InvalidMetric
	for _, m := range metrics {
		if inv, ok := r.checkOne(m); ok {
			result = append(result, inv)
		}
	}
	return result
}

func (r *CandidateDeprecatedRule) checkOne(m model.Metric) (model.InvalidMetric, bool) {
	var evidences []deprecationEvidence

	// ── Phase 1A：HELP 文本显式声明 ──────────────────────────────
	if m.HelpText != "" {
		lower := strings.ToLower(m.HelpText)
		for _, kw := range []string{"deprecated", "obsolete", "do not use", "will be removed", "replaced by"} {
			if strings.Contains(lower, kw) {
				evidences = append(evidences, deprecationEvidence{
					signal:     "help_text",
					confidence: model.ConfidenceHigh,
					desc:       fmt.Sprintf("HELP 文本显式声明废弃: %q", m.HelpText),
				})
				break
			}
		}
	}

	// ── Phase 1B：生命周期标签 ────────────────────────────────────
	for _, l := range m.Labels {
		key := strings.ToLower(l.Key)
		val := strings.ToLower(l.Value)
		var desc string
		switch {
		case key == "deprecated" && (val == "true" || val == "1"):
			desc = fmt.Sprintf("携带废弃标签 %s=%q", l.Key, l.Value)
		case key == "sunset_date":
			desc = fmt.Sprintf("携带下线日期标签 %s=%q", l.Key, l.Value)
		case key == "replaced_by":
			desc = fmt.Sprintf("携带替换关系标签 %s=%q，建议迁移至该指标", l.Key, l.Value)
		}
		if desc != "" {
			evidences = append(evidences, deprecationEvidence{
				signal:     "lifecycle_label",
				confidence: model.ConfidenceHigh,
				desc:       desc,
			})
			break
		}
	}

	// ── Phase 1C：名称启发式（弱信号）────────────────────────────
	nameHit := r.checkNameHeuristics(m.Name)
	if nameHit != "" {
		evidences = append(evidences, deprecationEvidence{
			signal:     "name_heuristic",
			confidence: model.ConfidenceLow,
			desc:       nameHit,
		})
	}

	// 无任何静态信号 → 不标记，直接返回
	if len(evidences) == 0 {
		return model.InvalidMetric{}, false
	}

	// ── Phase 2：规则引用动态分析（Prometheus 模式）───────────────
	if r.RuleReferences != nil {
		if refs, cited := r.RuleReferences[m.Name]; cited {
			// 被规则引用：说明仍有消费者，降低废弃可信度
			evidences = append(evidences, deprecationEvidence{
				signal:     "rule_cited",
				confidence: model.ConfidenceLow, // 被引用反而是弱反证
				desc: fmt.Sprintf(
					"仍被 %d 条 Prometheus 规则引用（%s），存在活跃消费者，请确认是否为计划中迁移",
					len(refs), strings.Join(firstN(refs, 3), ", "),
				),
			})
		} else {
			// 未被任何规则引用：补强废弃信号
			evidences = append(evidences, deprecationEvidence{
				signal:     "no_rule_reference",
				confidence: model.ConfidenceMedium,
				desc:       "未被任何 Prometheus 告警/记录规则引用（动态分析，不排除 Grafana 等外部消费）",
			})
		}
	}

	// ── Phase 3：综合定级 ─────────────────────────────────────────
	finalConf := synthesizeDeprecatedConfidence(evidences)

	// 构建 Issues：每条证据独立输出，便于人工复核
	issues := make([]model.DetectedIssue, 0, len(evidences))
	for _, ev := range evidences {
		issues = append(issues, model.DetectedIssue{
			Type:        model.TypeCandidateDeprecated,
			Severity:    model.SeverityMinor,
			Confidence:  ev.confidence,
			Description: ev.desc,
		})
	}

	// 整体置信度用第一条 issue 体现（控制台/报表读取 Issues[0].Confidence 作为整体展示）
	if len(issues) > 0 {
		issues[0].Confidence = finalConf
	}

	return model.InvalidMetric{
		Metric:      m,
		Issues:      issues,
		MaxSeverity: model.SeverityMinor,
	}, true
}

// checkNameHeuristics 返回命中的名称启发式描述，未命中返回空字符串
func (r *CandidateDeprecatedRule) checkNameHeuristics(name string) string {
	for _, p := range r.Prefixes {
		if strings.HasPrefix(name, p) {
			return fmt.Sprintf("名称前缀 %q 疑似废弃（弱信号，仅名称启发）", p)
		}
	}
	for _, s := range r.Suffixes {
		if strings.HasSuffix(name, s) {
			return fmt.Sprintf("名称后缀 %q 疑似废弃（弱信号，仅名称启发）", s)
		}
	}
	lower := strings.ToLower(name)
	for _, kw := range []string{"deprecated", "obsolete", "unused", "removed"} {
		if strings.Contains(lower, kw) {
			return fmt.Sprintf("名称含废弃关键字 %q（弱信号，仅名称启发）", kw)
		}
	}
	return ""
}

// synthesizeDeprecatedConfidence 根据全量证据综合确定最终置信度
//
// 规则：
//  1. 任意 HIGH 信号 → HIGH
//  2. MEDIUM booster (no_rule_reference) + 任意 LOW → MEDIUM
//  3. ≥2 个 LOW 信号（无 booster）→ MEDIUM（多个独立弱信号叠加）
//  4. 仅 1 个 LOW → LOW
func synthesizeDeprecatedConfidence(evidences []deprecationEvidence) model.Confidence {
	hasHigh := false
	hasMediumBooster := false
	lowCount := 0

	for _, ev := range evidences {
		switch ev.signal {
		case "rule_cited":
			// 被引用是反证，不纳入正向置信度计算
			continue
		}
		switch ev.confidence {
		case model.ConfidenceHigh:
			hasHigh = true
		case model.ConfidenceMedium:
			hasMediumBooster = true
		case model.ConfidenceLow:
			lowCount++
		}
	}

	switch {
	case hasHigh:
		return model.ConfidenceHigh
	case hasMediumBooster && lowCount > 0:
		return model.ConfidenceMedium
	case lowCount >= 2:
		return model.ConfidenceMedium
	default:
		return model.ConfidenceLow
	}
}

// firstN 取切片前 n 个元素
func firstN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
