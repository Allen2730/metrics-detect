package ingestion

import (
	"context"
	"fmt"
	"os"

	"github.com/memo/prometheus-analyzer/internal/model"
	"github.com/memo/prometheus-analyzer/internal/parser"
	"github.com/memo/prometheus-analyzer/pkg/logger"
	"go.uber.org/zap"
)

// FileReader 从本地文件读取 Prometheus 格式指标
type FileReader struct {
	FilePath string
}

func NewFileReader(path string) *FileReader {
	return &FileReader{FilePath: path}
}

func (r *FileReader) Fetch(_ context.Context) ([]model.Metric, error) {
	data, err := os.ReadFile(r.FilePath)
	if err != nil {
		return nil, fmt.Errorf("read metrics file %s: %w", r.FilePath, err)
	}
	metrics := parser.Parse(string(data))
	logger.Info("loaded metrics from file",
		zap.String("path", r.FilePath),
		zap.Int("count", len(metrics)),
	)
	return metrics, nil
}
