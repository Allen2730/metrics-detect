package parser

import (
	"bufio"
	"math"
	"strconv"
	"strings"

	"github.com/memo/prometheus-analyzer/internal/model"
)

// Parse 解析 Prometheus Exposition Format 文本，返回 Metric 列表。
// 同时解析 # HELP 注释并关联到对应指标，供废弃/冗余规则使用。
func Parse(text string) []model.Metric {
	// 第一轮：收集所有 # HELP metricName helpText
	helpMap := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if name, help, ok := parseHelpLine(line); ok {
			helpMap[name] = help
		}
	}

	// 第二轮：解析指标行，附加 HelpText
	var metrics []model.Metric
	scanner = bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m, ok := parseLine(line); ok {
			m.HelpText = helpMap[m.Name]
			metrics = append(metrics, m)
		}
	}
	return metrics
}

// parseHelpLine 解析 "# HELP metricName description" 格式
// 返回 (metricName, helpText, ok)
func parseHelpLine(line string) (string, string, bool) {
	if !strings.HasPrefix(line, "# HELP ") {
		return "", "", false
	}
	rest := strings.TrimPrefix(line, "# HELP ")
	idx := strings.IndexByte(rest, ' ')
	if idx == -1 {
		// "# HELP metricName" 没有描述文本
		return rest, "", true
	}
	return rest[:idx], rest[idx+1:], true
}

// parseLine 解析单行：name{k="v",...} value [timestamp]
func parseLine(line string) (model.Metric, bool) {
	m := model.Metric{RawLine: line}

	lbrace := strings.Index(line, "{")
	rbrace := strings.LastIndex(line, "}")

	if lbrace == -1 {
		// 无标签
		fields := strings.Fields(line)
		if len(fields) == 0 {
			return m, false
		}
		m.Name = fields[0]
		if len(fields) < 2 {
			return m, false
		}
		m.Value = parseFloat(fields[1])
		if len(fields) > 2 {
			m.Timestamp, _ = strconv.ParseInt(fields[2], 10, 64)
		}
	} else {
		m.Name = strings.TrimSpace(line[:lbrace])
		if rbrace == -1 {
			return m, false
		}
		labelStr := line[lbrace+1 : rbrace]
		m.Labels = parseLabels(labelStr)
		rest := strings.TrimSpace(line[rbrace+1:])
		parts := strings.Fields(rest)
		if len(parts) == 0 {
			return m, false
		}
		m.Value = parseFloat(parts[0])
		if len(parts) > 1 {
			m.Timestamp, _ = strconv.ParseInt(parts[1], 10, 64)
		}
	}
	return m, true
}

// parseLabels 解析标签字符串：k="v",k2="v2"
func parseLabels(s string) []model.Label {
	var labels []model.Label
	s = strings.TrimSpace(s)
	if s == "" {
		return labels
	}
	var parts []string
	depth := 0
	start := 0
	for i, ch := range s {
		switch ch {
		case '"':
			depth ^= 1
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])

	for _, part := range parts {
		part = strings.TrimSpace(part)
		eq := strings.Index(part, "=")
		if eq == -1 {
			continue
		}
		key := strings.TrimSpace(part[:eq])
		val := strings.Trim(strings.TrimSpace(part[eq+1:]), `"`)
		labels = append(labels, model.Label{Key: key, Value: val})
	}
	return labels
}

func parseFloat(s string) float64 {
	switch strings.ToLower(s) {
	case "+inf":
		return math.Inf(1)
	case "-inf":
		return math.Inf(-1)
	case "nan":
		return math.NaN()
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
