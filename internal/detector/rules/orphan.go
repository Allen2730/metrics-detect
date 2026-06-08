package rules

import (
	"fmt"
	"strings"

	"github.com/memo/prometheus-analyzer/internal/model"
)

// OrphanRule 检测孤儿指标（无匹配服务的指标）。
//
// 两种工作模式：
//   - 动态模式（ActiveServices 非空）：从指标标签中按 ServiceLabelKeys 优先级提取服务名，
//     与从 /api/v1/targets 拉取的存活服务集合对比，适用于 prometheus 接入模式。
//   - 静态模式（ActiveServices 为空）：退回到指标名前缀匹配，对比 KnownServices 配置白名单，
//     适用于本地文件接入模式。
type OrphanRule struct {
	// KnownServices 静态白名单，文件模式的兜底（小写）
	KnownServices []string
	// ActiveServices 动态服务集，由 /api/v1/targets 实时获取（小写），非空时优先使用
	ActiveServices map[string]struct{}
	// ServiceLabelKeys 按优先级排列的服务名标签 key 列表
	// 常见值：job、app、service、app_kubernetes_io_name、service_name、k8s_app
	ServiceLabelKeys []string
}

func (r *OrphanRule) Name() string { return "orphan" }

func (r *OrphanRule) Check(metrics []model.Metric) []model.InvalidMetric {
	if len(r.ActiveServices) > 0 {
		return r.checkDynamic(metrics)
	}
	return r.checkStatic(metrics)
}

// checkDynamic 动态模式：从指标标签提取服务名，与存活服务集对比
func (r *OrphanRule) checkDynamic(metrics []model.Metric) []model.InvalidMetric {
	var result []model.InvalidMetric
	for _, m := range metrics {
		if m.Name == "" {
			continue
		}
		svcName, usedKey := r.extractServiceName(m)
		if svcName == "" {
			// 标签中找不到任何服务标识，视为孤儿
			result = append(result, model.InvalidMetric{
				Metric: m,
				Issues: []model.DetectedIssue{{
					Type:     model.TypeOrphan,
					Severity: model.SeverityMajor,
					Description: fmt.Sprintf(
						"指标缺少服务标识标签（检查了 %s），无法归属到任何服务",
						strings.Join(r.ServiceLabelKeys, ", "),
					),
				}},
				MaxSeverity: model.SeverityMajor,
			})
			continue
		}
		if _, ok := r.ActiveServices[strings.ToLower(svcName)]; !ok {
			result = append(result, model.InvalidMetric{
				Metric: m,
				Issues: []model.DetectedIssue{{
					Type:     model.TypeOrphan,
					Severity: model.SeverityMajor,
					Description: fmt.Sprintf(
						"标签 %s=%q 对应的服务不在 Prometheus 存活 target 中，疑为孤儿指标",
						usedKey, svcName,
					),
				}},
				MaxSeverity: model.SeverityMajor,
			})
		}
	}
	return result
}

// checkStatic 静态模式：指标名前缀匹配已知服务白名单
func (r *OrphanRule) checkStatic(metrics []model.Metric) []model.InvalidMetric {
	knownSet := make(map[string]struct{}, len(r.KnownServices))
	for _, s := range r.KnownServices {
		knownSet[strings.ToLower(s)] = struct{}{}
	}

	var result []model.InvalidMetric
	for _, m := range metrics {
		if m.Name == "" {
			continue
		}
		// 优先从标签中查找服务名（如果指标携带了 job 等标签）
		svcName, usedKey := r.extractServiceName(m)
		if svcName != "" {
			if _, ok := knownSet[strings.ToLower(svcName)]; !ok {
				result = append(result, model.InvalidMetric{
					Metric: m,
					Issues: []model.DetectedIssue{{
						Type:     model.TypeOrphan,
						Severity: model.SeverityMajor,
						Description: fmt.Sprintf(
							"标签 %s=%q 不在已知服务列表中，疑为孤儿指标",
							usedKey, svcName,
						),
					}},
					MaxSeverity: model.SeverityMajor,
				})
			}
			continue
		}
		// 没有服务标签，退回前缀推断
		prefix := extractServicePrefix(m.Name)
		if _, ok := knownSet[prefix]; !ok {
			result = append(result, model.InvalidMetric{
				Metric: m,
				Issues: []model.DetectedIssue{{
					Type:     model.TypeOrphan,
					Severity: model.SeverityMajor,
					Description: fmt.Sprintf(
						"指标名前缀 %q 不在已知服务列表中，疑为孤儿指标（无服务标签，以名称前缀推断）",
						prefix,
					),
				}},
				MaxSeverity: model.SeverityMajor,
			})
		}
	}
	return result
}

// extractServiceName 按 ServiceLabelKeys 优先级依次尝试，返回第一个非空的服务名及所用的标签 key
func (r *OrphanRule) extractServiceName(m model.Metric) (name, key string) {
	// 先把 labels 转成 map，避免每次 O(n) 遍历
	labelMap := make(map[string]string, len(m.Labels))
	for _, l := range m.Labels {
		labelMap[l.Key] = l.Value
	}
	for _, k := range r.ServiceLabelKeys {
		if v, ok := labelMap[k]; ok && v != "" {
			return v, k
		}
	}
	return "", ""
}

// extractServicePrefix 取指标名第一段（首个 _ 之前）作为服务前缀
func extractServicePrefix(name string) string {
	idx := strings.Index(name, "_")
	if idx > 0 {
		return strings.ToLower(name[:idx])
	}
	return strings.ToLower(name)
}
