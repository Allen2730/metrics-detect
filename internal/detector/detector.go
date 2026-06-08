package detector

import (
	"sync"

	"github.com/memo/prometheus-analyzer/internal/config"
	"github.com/memo/prometheus-analyzer/internal/detector/rules"
	"github.com/memo/prometheus-analyzer/internal/model"
	"github.com/memo/prometheus-analyzer/pkg/logger"
	"go.uber.org/zap"
)

// defaultServiceLabelKeys 默认服务标签优先级，配置为空时使用
var defaultServiceLabelKeys = []string{
	"job",
	"app",
	"service",
	"app_kubernetes_io_name",
	"service_name",
	"k8s_app",
}

// Options 运行时动态数据，由 Prometheus 模式下的接入层提供
type Options struct {
	// ActiveServices 从 /api/v1/targets 拉取的存活服务集合，用于孤儿规则动态模式
	ActiveServices map[string]struct{}
	// RuleReferences 从 /api/v1/rules 解析的规则引用表
	// key=指标名，value=引用该指标的规则名列表
	// 废弃规则和冗余规则均使用
	RuleReferences map[string][]string
	// ActivityInfo 来自 /api/v1/query 的指标历史行为信息，用于冗余规则动态分析
	// key=指标名
	ActivityInfo map[string]model.MetricActivityInfo
}

// Detector 检测引擎，并行执行所有规则
type Detector struct {
	rules []rules.Rule
}

// New 创建检测器（文件模式，仅静态分析）
func New(cfg config.DetectionConfig) *Detector {
	return NewWithOptions(cfg, Options{})
}

// NewWithActiveServices 兼容旧调用，仅注入服务发现数据
func NewWithActiveServices(cfg config.DetectionConfig, activeServices map[string]struct{}) *Detector {
	return NewWithOptions(cfg, Options{ActiveServices: activeServices})
}

// NewWithOptions 创建检测器，注入所有运行时动态数据
func NewWithOptions(cfg config.DetectionConfig, opts Options) *Detector {
	svcLabelKeys := cfg.ServiceLabelKeys
	if len(svcLabelKeys) == 0 {
		svcLabelKeys = defaultServiceLabelKeys
	}

	return &Detector{
		rules: []rules.Rule{
			&rules.CandidateDeprecatedRule{
				Prefixes:       cfg.DeprecatedPrefixes,
				Suffixes:       cfg.DeprecatedSuffixes,
				RuleReferences: opts.RuleReferences,
			},
			&rules.DuplicateRule{},
			&rules.EmptyRule{},
			&rules.IllegalLabelRule{},
			&rules.OrphanRule{
				KnownServices:    cfg.KnownServiceNames,
				ActiveServices:   opts.ActiveServices,
				ServiceLabelKeys: svcLabelKeys,
			},
			&rules.HighCardinalityRule{Threshold: cfg.HighCardinalityThreshold},
			func() *rules.CandidateRedundantRule {
				r := rules.NewCandidateRedundantRule(cfg.RedundantPatterns)
				r.RuleReferences = opts.RuleReferences
				r.ActivityInfo = opts.ActivityInfo
				return r
			}(),
		},
	}
}

// Detect 对全量指标并行执行所有规则，合并结果
func (d *Detector) Detect(metrics []model.Metric) []model.InvalidMetric {
	type ruleResult struct {
		name    string
		results []model.InvalidMetric
	}

	resultCh := make(chan ruleResult, len(d.rules))
	var wg sync.WaitGroup

	for _, r := range d.rules {
		wg.Add(1)
		go func(r rules.Rule) {
			defer wg.Done()
			res := r.Check(metrics)
			resultCh <- ruleResult{name: r.Name(), results: res}
			logger.Debug("rule check done", zap.String("rule", r.Name()), zap.Int("hits", len(res)))
		}(r)
	}

	wg.Wait()
	close(resultCh)

	// 合并：同一条 Metric 可能被多个规则命中，合并 Issues
	merged := make(map[string]*model.InvalidMetric)
	for rr := range resultCh {
		for _, inv := range rr.results {
			key := inv.Metric.RawLine
			if key == "" {
				key = inv.Metric.Name + "|" + inv.Metric.RawLine
			}
			if existing, ok := merged[key]; ok {
				existing.Issues = append(existing.Issues, inv.Issues...)
				if model.SeverityOrder[inv.MaxSeverity] > model.SeverityOrder[existing.MaxSeverity] {
					existing.MaxSeverity = inv.MaxSeverity
				}
			} else {
				cp := inv
				merged[key] = &cp
			}
		}
	}

	result := make([]model.InvalidMetric, 0, len(merged))
	for _, v := range merged {
		result = append(result, *v)
	}

	logger.Info("detection complete",
		zap.Int("total_metrics", len(metrics)),
		zap.Int("invalid_metrics", len(result)),
	)
	return result
}
