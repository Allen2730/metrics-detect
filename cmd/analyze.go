package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/memo/prometheus-analyzer/internal/ai"
	"github.com/memo/prometheus-analyzer/internal/ai/claude"
	"github.com/memo/prometheus-analyzer/internal/ai/deepseek"
	"github.com/memo/prometheus-analyzer/internal/config"
	"github.com/memo/prometheus-analyzer/internal/detector"
	"github.com/memo/prometheus-analyzer/internal/ingestion"
	"github.com/memo/prometheus-analyzer/internal/model"
	"github.com/memo/prometheus-analyzer/internal/relabel"
	"github.com/memo/prometheus-analyzer/internal/reporter"
	"github.com/memo/prometheus-analyzer/internal/statistics"
	"github.com/memo/prometheus-analyzer/pkg/logger"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

var (
	flagNoAI     bool
	flagOutput   string
	flagInput    string
	flagProvider string
	flagAPIKey   string
	flagRelabel  bool // 是否生成 relabel 规则文件
)

var analyzeCmd = &cobra.Command{
	Use:   "analyze",
	Short: "执行完整指标分析流程",
	Long:  "读取指标数据，执行无效指标检测，AI 智能分析，输出多格式报告",
	RunE:  runAnalyze,
}

func init() {
	rootCmd.AddCommand(analyzeCmd)
	analyzeCmd.Flags().BoolVar(&flagNoAI, "no-ai", false, "跳过 AI 分析，仅输出统计结果")
	analyzeCmd.Flags().StringVar(&flagOutput, "output", "console,markdown,excel", "输出格式，逗号分隔: console,markdown,excel")
	analyzeCmd.Flags().StringVar(&flagInput, "input", "", "覆盖配置文件中的输入文件路径")
	analyzeCmd.Flags().StringVar(&flagProvider, "provider", "", "覆盖配置文件中的 AI provider (claude|deepseek)")
	analyzeCmd.Flags().StringVar(&flagAPIKey, "api-key", "", "覆盖 AI API Key（优先级低于环境变量）")
	analyzeCmd.Flags().BoolVar(&flagRelabel, "relabel", false, "生成 Prometheus metric_relabel_configs 规则文件")
}

func runAnalyze(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// CLI flags 覆盖配置
	if flagInput != "" {
		cfg.Ingestion.FilePath = flagInput
		cfg.Ingestion.Mode = "file"
	}
	if flagProvider != "" {
		cfg.AI.Provider = flagProvider
	}
	if flagAPIKey != "" && cfg.AI.APIKey == "" {
		cfg.AI.APIKey = flagAPIKey
	}
	if flagNoAI {
		cfg.AI.Enabled = false
	}

	// 初始化日志
	if err := os.MkdirAll(cfg.Report.OutputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if err := logger.Init(cfg.Log.Level, cfg.Log.File); err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer logger.Sync()

	logger.Info("analysis started",
		zap.String("mode", cfg.Ingestion.Mode),
		zap.Object("ai", cfg.AI),
	)

	// 1. 数据接入
	ing := buildIngester(cfg)
	metrics, err := ing.Fetch(context.Background())
	if err != nil {
		return fmt.Errorf("fetch metrics: %w", err)
	}
	fmt.Printf("\n已加载指标 %d 条\n", len(metrics))

	// 2. Prometheus 模式下并行拉取：存活服务（孤儿检测）+ 规则引用（废弃检测）
	opts := detector.Options{}
	if cfg.Ingestion.Mode == "prometheus" {
		if promClient, ok := ing.(*ingestion.PrometheusClient); ok {
			// 2a. 拉取存活 target，用于孤儿规则动态模式
			svcKeys := cfg.Detection.ServiceLabelKeys
			activeServices, svcErr := promClient.FetchActiveServices(context.Background(), svcKeys)
			if svcErr != nil {
				logger.Warn("fetch active services failed, fallback to static whitelist", zap.Error(svcErr))
			} else {
				fmt.Printf("发现存活服务 %d 个（来自 /api/v1/targets）\n", len(activeServices))
				opts.ActiveServices = activeServices
			}

			// 2b. 拉取规则引用，废弃规则和冗余规则共用
			ruleRefs, ruleErr := promClient.FetchRuleReferences(context.Background())
			if ruleErr != nil {
				logger.Warn("fetch rule references failed, skipping dynamic analysis", zap.Error(ruleErr))
			} else {
				fmt.Printf("加载规则引用 %d 个指标（来自 /api/v1/rules）\n", len(ruleRefs))
				opts.RuleReferences = ruleRefs
			}

			// 2c. 预筛冗余候选 → 批量查历史行为（仅对候选集，避免全量查询）
			candidates := preFilterRedundantCandidates(metrics, cfg.Detection, ruleRefs)
			if len(candidates) > 0 {
				activityInfo, actErr := promClient.FetchMetricActivity(context.Background(), candidates)
				if actErr != nil {
					logger.Warn("fetch metric activity failed, skipping historical behavior analysis", zap.Error(actErr))
				} else {
					fmt.Printf("获取历史行为数据 %d 个候选指标（来自 /api/v1/query）\n", len(activityInfo))
					opts.ActivityInfo = activityInfo
				}
			}
		}
	}

	det := detector.NewWithOptions(cfg.Detection, opts)
	invalids := det.Detect(metrics)
	fmt.Printf("检测到无效指标 %d 条\n", len(invalids))

	// 3. 统计
	stats := statistics.Calculate(metrics, invalids)

	// 4. AI 分析
	var aiResult model.AIAnalysisResult
	if cfg.AI.Enabled {
		provider, effectiveModel, err := buildProvider(cfg.AI)
		if err != nil {
			logger.Warn("AI provider init failed, skipping AI analysis", zap.Error(err))
		} else {
			analyzer := ai.NewAnalyzer(provider, effectiveModel, cfg.AI.Timeout)
			fmt.Println("正在进行 AI 智能分析，请稍候...")
			aiResult, err = analyzer.Analyze(stats, invalids)
			if err != nil {
				logger.Warn("AI analysis failed", zap.Error(err))
			}
		}
	}

	// 5. 生成 Relabel 规则文件（可选）
	if flagRelabel {
		gen := relabel.New()
		ruleSet := gen.Generate(invalids)
		relabelPath := filepath.Join(cfg.Report.OutputDir, "relabel_rules.yaml")
		if err := writeRelabelFile(relabelPath, ruleSet); err != nil {
			logger.Error("write relabel file failed", zap.Error(err))
		} else {
			fmt.Printf("Relabel 规则已输出（%d 条，其中 %d 条需审核）: %s\n",
				ruleSet.TotalRules, ruleSet.DisabledRules, relabelPath)
		}
	}

	// 6. 报告输出
	outputs := strings.Split(flagOutput, ",")
	for _, out := range outputs {
		out = strings.TrimSpace(out)
		switch out {
		case "console":
			r := reporter.NewConsoleReporter()
			_ = r.Write(stats, aiResult)
		case "markdown":
			if cfg.Report.EnableMarkdown {
				r := reporter.NewMarkdownReporter(cfg.Report.OutputDir)
				if err := r.Write(stats, aiResult); err != nil {
					logger.Error("markdown report failed", zap.Error(err))
				}
			}
		case "excel":
			if cfg.Report.EnableExcel {
				r := reporter.NewExcelReporter(cfg.Report.OutputDir)
				if err := r.Write(stats, aiResult); err != nil {
					logger.Error("excel report failed", zap.Error(err))
				}
			}
		}
	}

	return nil
}

func writeRelabelFile(path string, rs relabel.RuleSet) error {
	content := rs.ToYAML("  ")
	return os.WriteFile(path, []byte(content), 0o644)
}

func buildIngester(cfg *config.Config) ingestion.Ingester {
	if cfg.Ingestion.Mode == "prometheus" {
		return ingestion.NewPrometheusClient(cfg.Ingestion.PrometheusURL)
	}
	return ingestion.NewFileReader(cfg.Ingestion.FilePath)
}

// preFilterRedundantCandidates 预筛冗余候选指标名，供后续历史行为查询使用。
// 命中任一条件即纳入候选：名称匹配冗余模式、携带测试环境标签、未被任何规则引用。
// 此函数与 redundant.go 的静态检测逻辑部分重合，目的是在历史查询前减少查询范围。
func preFilterRedundantCandidates(
	metrics []model.Metric,
	cfg config.DetectionConfig,
	ruleRefs map[string][]string,
) []string {
	compiled := make([]*regexp.Regexp, 0)
	for _, p := range cfg.RedundantPatterns {
		if re, err := regexp.Compile(p); err == nil {
			compiled = append(compiled, re)
		}
	}

	testEnv := map[string]struct{}{
		"test": {}, "testing": {}, "dev": {}, "development": {},
		"debug": {}, "local": {}, "ci": {}, "sandbox": {},
	}

	seen := make(map[string]struct{})
	for _, m := range metrics {
		if _, ok := seen[m.Name]; ok {
			continue
		}
		candidate := false

		// 名称匹配冗余模式
		for _, re := range compiled {
			if re.MatchString(m.Name) {
				candidate = true
				break
			}
		}

		// 测试环境标签
		if !candidate {
			for _, l := range m.Labels {
				if l.Key == "env" || l.Key == "environment" {
					if _, ok := testEnv[strings.ToLower(l.Value)]; ok {
						candidate = true
						break
					}
				}
			}
		}

		// 未被任何规则引用
		if !candidate && ruleRefs != nil {
			if _, cited := ruleRefs[m.Name]; !cited {
				candidate = true
			}
		}

		if candidate {
			seen[m.Name] = struct{}{}
		}
	}

	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	return names
}

// buildProvider 构造 LLM 提供者，同时返回实际使用的模型名。
// 当配置的 model 属于另一个 provider（如 provider=deepseek 却配了 claude-* 的模型名），
// 自动切换为当前 provider 的默认模型，避免跨 provider 的模型名混用。
func buildProvider(cfg config.AIConfig) (ai.LLMProvider, string, error) {
	if cfg.APIKey == "" {
		return nil, "", fmt.Errorf("API key not configured for provider '%s'", cfg.Provider)
	}
	switch strings.ToLower(cfg.Provider) {
	case "deepseek":
		model := cfg.Model
		if model == "" || strings.HasPrefix(strings.ToLower(model), "claude") {
			model = "deepseek-v4-flash"
		}
		return deepseek.New(cfg.APIKey, model), model, nil
	default: // claude
		model := cfg.Model
		if model == "" || strings.HasPrefix(strings.ToLower(model), "deepseek") {
			model = "claude-sonnet-4-6"
		}
		return claude.New(cfg.APIKey, model), model, nil
	}
}
