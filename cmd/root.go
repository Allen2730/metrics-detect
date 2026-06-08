package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "prometheus-analyzer",
	Short: "Prometheus 无效指标智能分析工具",
	Long: `基于 AI 的 Prometheus 无效指标统计与智能识别分析工具

支持识别废弃、重复、空值、违规标签、孤儿、高基数、冗余等七类无效指标，
结合 AI 大模型进行根因分析并输出监控治理报告。`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "config.json", "配置文件路径")
}
