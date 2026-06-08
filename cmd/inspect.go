package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/memo/prometheus-analyzer/internal/config"
	"github.com/memo/prometheus-analyzer/internal/detector"
	"github.com/memo/prometheus-analyzer/internal/model"
	"github.com/memo/prometheus-analyzer/pkg/logger"
	"github.com/spf13/cobra"
)

var (
	flagMetricName string
	flagLabelExpr  string
)

var inspectCmd = &cobra.Command{
	Use:   "inspect",
	Short: "单独分析指定指标或标签（交互模式）",
	Long:  "指定指标名称或标签过滤条件，单独分析匹配的指标有效性",
	RunE:  runInspect,
}

func init() {
	rootCmd.AddCommand(inspectCmd)
	inspectCmd.Flags().StringVar(&flagMetricName, "metric", "", "指标名称（支持前缀匹配）")
	inspectCmd.Flags().StringVar(&flagLabelExpr, "label", "", "标签过滤，格式: key=value")
}

func runInspect(cmd *cobra.Command, args []string) error {
	if flagMetricName == "" && flagLabelExpr == "" {
		return fmt.Errorf("至少指定 --metric 或 --label 参数")
	}

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	_ = logger.Init(cfg.Log.Level, "")

	ing := buildIngester(cfg)
	metrics, err := ing.Fetch(context.Background())
	if err != nil {
		return fmt.Errorf("fetch metrics: %w", err)
	}

	// 过滤
	filtered := filterMetrics(metrics, flagMetricName, flagLabelExpr)
	if len(filtered) == 0 {
		fmt.Println("未找到匹配的指标")
		return nil
	}

	fmt.Printf("\n匹配到 %d 条指标，开始分析...\n\n", len(filtered))

	det := detector.New(cfg.Detection)
	invalids := det.Detect(filtered)

	if len(invalids) == 0 {
		fmt.Printf("✅ 所有 %d 条匹配指标均有效\n", len(filtered))
		return nil
	}

	fmt.Printf("发现 %d 条无效指标：\n\n", len(invalids))
	for i, inv := range invalids {
		sevColor := severityColorText(inv.MaxSeverity)
		fmt.Printf("%d. [%s%s\033[0m] %s\n", i+1, sevColor, inv.MaxSeverity, inv.Metric.Name)
		for _, issue := range inv.Issues {
			fmt.Printf("   ↳ %s%s\n", confidencePrefix(issue.Confidence), issue.Description)
		}
		fmt.Println()
	}
	return nil
}

func filterMetrics(metrics []model.Metric, name, labelExpr string) []model.Metric {
	var result []model.Metric
	for _, m := range metrics {
		if name != "" && !strings.Contains(m.Name, name) {
			continue
		}
		if labelExpr != "" {
			parts := strings.SplitN(labelExpr, "=", 2)
			if len(parts) == 2 {
				matched := false
				for _, l := range m.Labels {
					if l.Key == parts[0] && l.Value == parts[1] {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}
		}
		result = append(result, m)
	}
	return result
}

func confidencePrefix(c model.Confidence) string {
	switch c {
	case model.ConfidenceMedium:
		return "[置信度:中] "
	case model.ConfidenceLow:
		return "[置信度:低] "
	default:
		return ""
	}
}

func severityColorText(s model.Severity) string {
	switch s {
	case model.SeverityCritical:
		return "\033[31m"
	case model.SeverityMajor:
		return "\033[33m"
	default:
		return "\033[32m"
	}
}
