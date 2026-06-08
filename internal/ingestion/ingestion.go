package ingestion

import (
	"context"

	"github.com/memo/prometheus-analyzer/internal/model"
)

// Ingester 数据接入接口
type Ingester interface {
	// Fetch 拉取原始指标，返回解析后的 Metric 列表
	Fetch(ctx context.Context) ([]model.Metric, error)
}
