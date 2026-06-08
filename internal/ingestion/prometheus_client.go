package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/memo/prometheus-analyzer/internal/model"
	"github.com/memo/prometheus-analyzer/pkg/logger"
	"go.uber.org/zap"
)

// PrometheusClient 通过 Prometheus HTTP API 拉取指标元数据，同时支持服务发现
type PrometheusClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

func NewPrometheusClient(baseURL string) *PrometheusClient {
	return &PrometheusClient{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

type seriesResponse struct {
	Status string              `json:"status"`
	Data   []map[string]string `json:"data"`
}

func (c *PrometheusClient) Fetch(ctx context.Context) ([]model.Metric, error) {
	url := fmt.Sprintf("%s/api/v1/series?match[]={__name__=~\".+\"}", c.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch series from %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()

	var result seriesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if result.Status != "success" {
		return nil, fmt.Errorf("prometheus API returned status: %s", result.Status)
	}

	var metrics []model.Metric
	for _, series := range result.Data {
		m := model.Metric{}
		for k, v := range series {
			if k == "__name__" {
				m.Name = v
			} else {
				m.Labels = append(m.Labels, model.Label{Key: k, Value: v})
			}
		}
		metrics = append(metrics, m)
	}

	logger.Info("loaded metrics from prometheus",
		zap.String("url", c.BaseURL),
		zap.Int("count", len(metrics)),
	)
	return metrics, nil
}

// rulesResponse /api/v1/rules 响应结构
type rulesResponse struct {
	Status string `json:"status"`
	Data   struct {
		Groups []struct {
			Rules []struct {
				Type  string `json:"type"`  // "alerting" | "recording"
				Name  string `json:"name"`  // recording rule 的输出指标名
				Query string `json:"query"` // PromQL 表达式
			} `json:"rules"`
		} `json:"groups"`
	} `json:"data"`
}

// FetchRuleReferences 调用 /api/v1/rules 获取所有规则中被引用的指标名集合。
// 返回的 map key 为指标名，value 为引用该指标的规则名列表（用于描述信息）。
// 指标名通过词边界匹配从 PromQL 表达式中提取，过滤 PromQL 内置函数/关键字。
func (c *PrometheusClient) FetchRuleReferences(ctx context.Context) (map[string][]string, error) {
	url := fmt.Sprintf("%s/api/v1/rules", c.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create rules request: %w", err)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch rules from %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()

	var result rulesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode rules response: %w", err)
	}
	if result.Status != "success" {
		return nil, fmt.Errorf("prometheus rules API returned status: %s", result.Status)
	}

	// metricName → 引用它的规则名列表
	references := make(map[string][]string)
	for _, g := range result.Data.Groups {
		for _, r := range g.Rules {
			ruleName := r.Name
			if ruleName == "" {
				ruleName = r.Type + "_rule"
			}
			for _, metricName := range extractMetricNames(r.Query) {
				references[metricName] = append(references[metricName], ruleName)
			}
		}
	}

	logger.Info("fetched rule references from prometheus",
		zap.String("url", c.BaseURL),
		zap.Int("referenced_metrics", len(references)),
	)
	return references, nil
}

// promQLFunctions PromQL 内置函数和关键字，提取指标名时需过滤
var promQLFunctions = map[string]struct{}{
	"sum": {}, "avg": {}, "max": {}, "min": {}, "count": {}, "count_values": {},
	"stddev": {}, "stdvar": {}, "topk": {}, "bottomk": {}, "quantile": {},
	"rate": {}, "irate": {}, "increase": {}, "delta": {}, "idelta": {},
	"resets": {}, "changes": {}, "deriv": {}, "predict_linear": {},
	"histogram_quantile": {}, "holt_winters": {},
	"label_replace": {}, "label_join": {},
	"abs": {}, "ceil": {}, "floor": {}, "round": {}, "sqrt": {}, "exp": {},
	"ln": {}, "log2": {}, "log10": {}, "sgn": {},
	"timestamp": {}, "vector": {}, "scalar": {}, "time": {},
	"sort": {}, "sort_desc": {}, "absent": {}, "absent_over_time": {},
	"max_over_time": {}, "min_over_time": {}, "avg_over_time": {},
	"sum_over_time": {}, "count_over_time": {}, "last_over_time": {},
	"present_over_time": {}, "quantile_over_time": {},
	"stddev_over_time": {}, "stdvar_over_time": {},
	"by": {}, "without": {}, "on": {}, "ignoring": {},
	"group_left": {}, "group_right": {}, "offset": {}, "bool": {},
	"and": {}, "or": {}, "unless": {}, "inf": {}, "nan": {},
}

// identRe 匹配可能是指标名的标识符（含冒号，Prometheus 允许 : 在记录规则名中）
var identRe = regexp.MustCompile(`[a-zA-Z_:][a-zA-Z0-9_:]*`)

// extractMetricNames 从 PromQL 表达式中提取指标名，过滤内置函数和关键字
func extractMetricNames(expr string) []string {
	tokens := identRe.FindAllString(expr, -1)
	seen := make(map[string]struct{})
	var result []string
	for _, tok := range tokens {
		if _, isKeyword := promQLFunctions[strings.ToLower(tok)]; isKeyword {
			continue
		}
		// 纯数字串（不应有，但防御）
		if tok == "" {
			continue
		}
		if _, ok := seen[tok]; !ok {
			seen[tok] = struct{}{}
			result = append(result, tok)
		}
	}
	return result
}

// targetsResponse /api/v1/targets 响应结构
type targetsResponse struct {
	Status string `json:"status"`
	Data   struct {
		ActiveTargets []struct {
			Labels map[string]string `json:"labels"`
			Health string            `json:"health"` // "up" | "down" | "unknown"
		} `json:"activeTargets"`
	} `json:"data"`
}

// FetchActiveServices 调用 /api/v1/targets 获取当前存活（health=up）的服务集合。
// 返回的 map key 为服务名（小写），value 为空结构体，供孤儿规则做 O(1) 查找。
// 服务名从各 target 的 labels 中依次尝试 labelKeys 指定的优先级列表提取。
func (c *PrometheusClient) FetchActiveServices(ctx context.Context, labelKeys []string) (map[string]struct{}, error) {
	url := fmt.Sprintf("%s/api/v1/targets?state=active", c.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create targets request: %w", err)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch targets from %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()

	var result targetsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode targets response: %w", err)
	}
	if result.Status != "success" {
		return nil, fmt.Errorf("prometheus targets API returned status: %s", result.Status)
	}

	services := make(map[string]struct{})
	for _, target := range result.Data.ActiveTargets {
		// 只收录健康的 target
		if target.Health != "up" {
			continue
		}
		name := extractServiceFromLabels(target.Labels, labelKeys)
		if name != "" {
			services[strings.ToLower(name)] = struct{}{}
		}
	}

	logger.Info("discovered active services from prometheus targets",
		zap.String("url", c.BaseURL),
		zap.Int("service_count", len(services)),
	)
	return services, nil
}

// instantQueryResponse /api/v1/query 响应结构
type instantQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"` // [timestamp, "value_string"]
		} `json:"result"`
	} `json:"data"`
}

const activityBatchSize = 20 // 每批最多查询的指标数，避免 regex 过长
const activityMaxCandidates = 200 // 最多处理的候选指标数

// FetchMetricActivity 批量查询候选指标的历史行为，每批使用正则批量查询减少请求数。
// 对每个候选指标执行三类检测：
//   - IsStale：近 5 分钟无新样本
//   - IsAlwaysZero：近 7 天最大值为 0
//   - IsStatic：近 7 天值无变化（非零常量）
func (c *PrometheusClient) FetchMetricActivity(ctx context.Context, names []string) (map[string]model.MetricActivityInfo, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if len(names) > activityMaxCandidates {
		names = names[:activityMaxCandidates]
		logger.Warn("too many redundant candidates, truncated",
			zap.Int("limit", activityMaxCandidates))
	}

	// 初始化全部为 stale，后续查到有数据再更新
	result := make(map[string]model.MetricActivityInfo, len(names))
	for _, n := range names {
		result[n] = model.MetricActivityInfo{IsStale: true}
	}

	for i := 0; i < len(names); i += activityBatchSize {
		end := i + activityBatchSize
		if end > len(names) {
			end = len(names)
		}
		if err := c.fetchActivityBatch(ctx, names[i:end], result); err != nil {
			return nil, err
		}
	}

	logger.Info("fetched metric activity info",
		zap.String("url", c.BaseURL),
		zap.Int("candidates", len(names)),
	)
	return result, nil
}

func (c *PrometheusClient) fetchActivityBatch(ctx context.Context, names []string, result map[string]model.MetricActivityInfo) error {
	// 转义名称中的正则特殊字符（Prometheus 指标名通常只含 [a-zA-Z0-9_:]，但防御性处理）
	escaped := make([]string, len(names))
	for i, n := range names {
		escaped[i] = regexp.QuoteMeta(n)
	}
	nameRegex := strings.Join(escaped, "|")
	selector := fmt.Sprintf(`{__name__=~"^(%s)$"}`, nameRegex)

	// Query 1：stale 检测（近 5 分钟有无样本）
	activeNames, err := c.queryMetricNameSet(ctx,
		fmt.Sprintf(`count_over_time(%s[5m])`, selector))
	if err != nil {
		return fmt.Errorf("stale check query: %w", err)
	}
	for n := range activeNames {
		info := result[n]
		info.IsStale = false
		result[n] = info
	}

	// Query 2：近 7 天最大值（检测长期为零）
	maxValues, err := c.queryMetricMaxByName(ctx,
		fmt.Sprintf(`max by(__name__)(max_over_time(%s[7d]))`, selector))
	if err != nil {
		return fmt.Errorf("always-zero check query: %w", err)
	}
	for n, maxVal := range maxValues {
		info := result[n]
		if maxVal == 0 {
			info.IsAlwaysZero = true
		}
		result[n] = info
	}

	// Query 3：近 7 天变化次数（检测恒定值）
	changeValues, err := c.queryMetricMaxByName(ctx,
		fmt.Sprintf(`max by(__name__)(changes(%s[7d]))`, selector))
	if err != nil {
		return fmt.Errorf("static check query: %w", err)
	}
	for n, changes := range changeValues {
		info := result[n]
		if changes == 0 && !info.IsAlwaysZero {
			info.IsStatic = true
		}
		result[n] = info
	}

	return nil
}

// queryMetricNameSet 执行即时查询，返回结果中出现的所有 __name__ 值的集合
func (c *PrometheusClient) queryMetricNameSet(ctx context.Context, query string) (map[string]struct{}, error) {
	resp, err := c.queryInstant(ctx, query)
	if err != nil {
		return nil, err
	}
	names := make(map[string]struct{})
	for _, r := range resp.Data.Result {
		if n, ok := r.Metric["__name__"]; ok {
			names[n] = struct{}{}
		}
	}
	return names, nil
}

// queryMetricMaxByName 执行即时查询，按 __name__ 聚合取最大值
func (c *PrometheusClient) queryMetricMaxByName(ctx context.Context, query string) (map[string]float64, error) {
	resp, err := c.queryInstant(ctx, query)
	if err != nil {
		return nil, err
	}
	values := make(map[string]float64)
	for _, r := range resp.Data.Result {
		n, ok := r.Metric["__name__"]
		if !ok {
			continue
		}
		if len(r.Value) < 2 {
			continue
		}
		var valStr string
		if err := json.Unmarshal(r.Value[1], &valStr); err != nil {
			continue
		}
		var f float64
		if _, err := fmt.Sscanf(valStr, "%f", &f); err != nil {
			continue
		}
		if cur, exists := values[n]; !exists || f > cur {
			values[n] = f
		}
	}
	return values, nil
}

// queryInstant 调用 /api/v1/query 执行即时查询
func (c *PrometheusClient) queryInstant(ctx context.Context, query string) (*instantQueryResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/api/v1/query", c.BaseURL), nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("query", query)
	req.URL.RawQuery = q.Encode()

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("instant query failed: %w", err)
	}
	defer resp.Body.Close()

	var result instantQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode instant query response: %w", err)
	}
	if result.Status != "success" {
		return nil, fmt.Errorf("prometheus query returned status: %s", result.Status)
	}
	return &result, nil
}

// extractServiceFromLabels 按优先级依次尝试 labelKeys，返回第一个非空的值
func extractServiceFromLabels(labels map[string]string, labelKeys []string) string {
	for _, key := range labelKeys {
		if v, ok := labels[key]; ok && v != "" {
			return v
		}
	}
	return ""
}
