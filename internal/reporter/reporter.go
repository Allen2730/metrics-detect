package reporter

import (
	"github.com/memo/prometheus-analyzer/internal/model"
)

// Reporter 报告输出接口
type Reporter interface {
	Write(stats model.StatisticsReport, ai model.AIAnalysisResult) error
}
