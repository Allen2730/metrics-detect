package model

// Label 标签键值对
type Label struct {
	Key   string
	Value string
}

// Metric 解析后的单条指标
type Metric struct {
	Name      string
	Labels    []Label
	Value     float64
	Timestamp int64  // Unix ms，0 表示无时间戳
	RawLine   string // 原始文本行，用于日志定位
	HelpText  string // # HELP 行内容，可为空
}

// InvalidType 无效指标类型
type InvalidType string

const (
	// 确定性问题：有明确的数据模型或行为证据
	TypeDuplicate       InvalidType = "duplicate"        // 重复指标
	TypeEmpty           InvalidType = "empty"            // 空名称/标签/值
	TypeIllegalLabel    InvalidType = "illegal_label"    // 违规字符标签
	TypeOrphan          InvalidType = "orphan"           // 孤儿指标
	TypeHighCardinality InvalidType = "high_cardinality" // 高基数指标

	// 候选问题：基于多维信号推断，存在误报可能，需人工复核
	TypeCandidateDeprecated InvalidType = "candidate_deprecated" // 疑似废弃（命名/HELP/标签推断）
	TypeCandidateRedundant  InvalidType = "candidate_redundant"  // 疑似冗余（命名/环境/行为推断）
)

// InvalidTypeDesc 类型中文描述
var InvalidTypeDesc = map[InvalidType]string{
	TypeDuplicate:           "重复指标",
	TypeEmpty:               "空值指标",
	TypeIllegalLabel:        "违规标签指标",
	TypeOrphan:              "孤儿指标",
	TypeHighCardinality:     "高基数指标",
	TypeCandidateDeprecated: "疑似废弃指标",
	TypeCandidateRedundant:  "疑似冗余指标",
}

// Confidence 检测置信度，说明证据强度
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"   // 强证据：HELP 显式声明、生命周期标签
	ConfidenceMedium Confidence = "medium" // 中等证据：环境标签、行为特征
	ConfidenceLow    Confidence = "low"    // 弱证据：仅名称启发式匹配
)

// Severity 风险等级
type Severity string

const (
	SeverityCritical Severity = "critical" // 严重
	SeverityMajor    Severity = "major"    // 一般
	SeverityMinor    Severity = "minor"    // 轻微
)

var SeverityOrder = map[Severity]int{
	SeverityCritical: 3,
	SeverityMajor:    2,
	SeverityMinor:    1,
}

// DetectedIssue 单条检测问题
type DetectedIssue struct {
	Type        InvalidType
	Severity    Severity
	Confidence  Confidence // 检测置信度
	Description string
}

// InvalidMetric 无效指标完整记录
type InvalidMetric struct {
	Metric      Metric
	Issues      []DetectedIssue
	MaxSeverity Severity
}

// LabelStat 标签统计
type LabelStat struct {
	LabelKey string
	Count    int
	Examples []string
}

// TypeStats 单类统计
type TypeStats struct {
	Count   int
	Percent float64
}

// StatisticsReport 统计报告
type StatisticsReport struct {
	TotalMetrics   int
	ValidMetrics   int
	InvalidMetrics int
	InvalidPercent float64

	ByType     map[InvalidType]TypeStats
	BySeverity map[Severity]int

	Top20Invalid       []InvalidMetric
	Top20IllegalLabels []LabelStat
}

// Pattern 主要问题模式（Phase 1 输出）
type Pattern struct {
	Name            string   `json:"pattern"`
	RootCause       string   `json:"root_cause"`
	AffectedMetrics []string `json:"affected_metrics"`
	Severity        string   `json:"severity"`   // critical / major / minor
	Confidence      string   `json:"confidence"` // confirmed / high / medium / low
	// confirmed = 确定性问题（高基数/空值/违规标签/重复/孤儿）
	// high/medium/low = 候选问题的置信度
}

// Deduction 健康评分扣分项（Phase 2 输出）
type Deduction struct {
	Reason string `json:"reason"`
	Points int    `json:"points"` // 负数
	Detail string `json:"detail"`
}

// ActionType 建议的操作类型，由置信度决定
type ActionType string

const (
	// ActionTypeDirect 直接执行：确定性问题，有充分证据，给出可直接操作的步骤
	ActionTypeDirect ActionType = "direct_action"
	// ActionTypeConfirmFirst 先确认再执行：候选问题置信度 HIGH，需先验证替代方案或所有权
	ActionTypeConfirmFirst ActionType = "confirm_first"
	// ActionTypeInvestigate 仅供调查：候选问题置信度 MEDIUM/LOW，只给调查方向，禁止给操作步骤
	ActionTypeInvestigate ActionType = "investigate_only"
)

// Suggestion 优化建议（Phase 2 输出）
type Suggestion struct {
	Priority      int        `json:"priority"`
	Action        string     `json:"action"`
	TargetMetrics []string   `json:"target_metrics"`
	Detail        string     `json:"detail"`
	Timeline      string     `json:"timeline"`    // 立即执行 / 计划执行 / 调查阶段
	ActionType    ActionType `json:"action_type"` // direct_action / confirm_first / investigate_only
}

// RelabelRule 生成的 Prometheus relabel 规则（Phase 2 输出）
type RelabelRule struct {
	Description string `json:"description"`
	Config      string `json:"config"`
}

// AIAnalysisResult AI 分析结果（结构化，两阶段合并）
type AIAnalysisResult struct {
	// Phase 1：根因分析
	HealthLevel   string    `json:"health_level"`   // 好 / 一般 / 差 / 极差
	HealthSummary string    `json:"health_summary"` // 健康状态概述
	MainPatterns  []Pattern `json:"main_patterns"`  // 主要问题模式

	// Phase 2：治理方案
	OverallScore    int           `json:"overall_score"`    // 0-100
	ScoreDeductions []Deduction   `json:"score_deductions"` // 扣分明细
	Suggestions     []Suggestion  `json:"suggestions"`      // 优化建议（按 Priority 排序）
	RelabelRules    []RelabelRule `json:"relabel_rules"`    // Relabel 规则建议

	// Meta
	Provider string `json:"provider"`
	Model    string `json:"model"`

	// 解析失败时的兜底原始文本
	RawPhase1 string `json:"raw_phase1,omitempty"`
	RawPhase2 string `json:"raw_phase2,omitempty"`
}

// MetricActivityInfo 指标历史行为信息，来自 Prometheus range query（仅 Prometheus 模式）
type MetricActivityInfo struct {
	IsStale      bool // 近 5 分钟无新样本（序列已 stale）
	IsAlwaysZero bool // 近 7 天最大值为 0（长期无意义上报）
	IsStatic     bool // 近 7 天值完全无变化但非零（恒定值，无信息量）
}

// ScanResult 一次完整扫描结果（用于历史对比序列化）
type ScanResult struct {
	ScanTime string           `json:"scan_time"`
	Stats    StatisticsReport `json:"stats"`
	AIResult AIAnalysisResult `json:"ai_result"`
}
