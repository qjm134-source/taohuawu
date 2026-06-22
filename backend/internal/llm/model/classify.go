package model

import (
	"strings"
	"unicode"
)

// TaskType 定义任务类型，用于根据任务特征选择合适的模型。
type TaskType string

const (
	TaskTypeGeneral    TaskType = "general"    // 通用对话
	TaskTypeCode       TaskType = "code"       // 代码相关
	TaskTypeReasoning  TaskType = "reasoning"  // 推理任务
	TaskTypeChinese    TaskType = "chinese"    // 中文内容
	TaskTypeLongText   TaskType = "longtext"   // 长文本处理
)

// ClassifyTask 根据消息内容识别任务类型。
// 使用关键词匹配启发式规则，优先级从高到低：
// 1. Code - 检测代码关键词和符号
// 2. Reasoning - 检测推理类关键词
// 3. Chinese - 检测中文字符比例
// 4. LongText - 检测文本长度
// 5. 默认为 General
func ClassifyTask(text string) TaskType {
	text = strings.ToLower(text)

	// 检测代码任务
	if isCodeTask(text) {
		return TaskTypeCode
	}

	// 检测推理任务
	if isReasoningTask(text) {
		return TaskTypeReasoning
	}

	// 检测中文内容
	if isChineseContent(text) {
		return TaskTypeChinese
	}

	// 检测长文本
	if isLongText(text) {
		return TaskTypeLongText
	}

	return TaskTypeGeneral
}

// isCodeTask 检测是否为代码相关任务。
func isCodeTask(text string) bool {
	codeKeywords := []string{
		"function", "class", "def ", "import ", "from ", "const ", "let ", "var ",
		"interface", "type ", "struct", "enum", "return", "print", "console.log",
		"npm install", "pip install", "git commit", "docker build",
		"bug", "debug", "error", "exception", "stack trace", "compile",
		".js", ".ts", ".py", ".go", ".java", ".cpp", ".rs", ".rb", ".php",
		"<div>", "<script>", "function(", "def ", "class ", "struct ",
		"算法", "数据结构", "代码", "编程", "函数", "类", "对象",
	}

	for _, kw := range codeKeywords {
		if strings.Contains(text, kw) {
			return true
		}
	}

	// 检测代码块标记
	if strings.Contains(text, "```") {
		return true
	}

	// 检测大量特殊字符（可能是代码）
	specialCount := 0
	for _, r := range text {
		if unicode.IsPunct(r) || unicode.IsSymbol(r) {
			specialCount++
		}
	}
	if len(text) > 0 && float64(specialCount)/float64(len(text)) > 0.3 {
		return true
	}

	return false
}

// isReasoningTask 检测是否为推理任务。
func isReasoningTask(text string) bool {
	reasoningKeywords := []string{
		"为什么", "how", "why", "explain", "explain", "reason",
		"分析", "比较", "区别", "差异", "关系",
		"assume", "suppose", "given that", "consider", "imply",
		"证明", "推导", "计算", "求解", "逻辑",
		"step", "思考", "推理", "推断", "结论",
		"problem", "solve", "solution", "answer", "question",
		"假设", "如果", "那么", "因此", "所以",
	}

	for _, kw := range reasoningKeywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// isChineseContent 检测是否主要为中文内容。
// 如果中文字符占比超过 30%，则认为是中文内容。
func isChineseContent(text string) bool {
	chineseCount := 0
	for _, r := range text {
		if isChineseChar(r) {
			chineseCount++
		}
	}

	if len(text) == 0 {
		return false
	}

	// 中文字符占比超过 30%
	return float64(chineseCount)/float64(len(text)) > 0.3
}

// isChineseChar 判断是否为中文字符。
func isChineseChar(r rune) bool {
	// CJK 统一表意文字范围
	return (r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0x20000 && r <= 0x2A6DF) ||
		(r >= 0x2A700 && r <= 0x2B73F) ||
		(r >= 0x2B740 && r <= 0x2B81F) ||
		(r >= 0x2B820 && r <= 0x2CEAF) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0x2F800 && r <= 0x2FA1F)
}

// isLongText 检测是否为长文本。
// Token 估算：每4字符约1token，中文按字节估算。
// 超过 2000 tokens 认为是长文本。
func isLongText(text string) bool {
	tokens := EstimateTokens(text)
	return tokens > 2000
}

// GetProviderCapabilities 返回每种任务类型推荐的 provider 类型。
// 基于 2026 年最新基准测试数据（MMLU、HumanEval、GSM8K、MT-Bench 等），
// 结合国内外模型表现，为不同任务类型推荐最优模型组合。
//
// Provider 命名约定：
// - "claude": Anthropic Claude 系列（Claude 3.5 Sonnet）
// - "openai": OpenAI GPT 系列（GPT-4o）
// - "glm": 智谱 GLM 系列（GLM-4）
// - "qwen": 通义千问系列（Qwen 2.0）
// - "gemini": Google Gemini 系列（Gemini 1.5 Pro/Flash）
//
// 任务类型优先级（从高到低）：Code > Reasoning > Chinese > LongText > General
// 模型列表按优先级排列，系统自动选择第一个可用的模型。
func GetProviderCapabilities() map[TaskType][]string {
	return map[TaskType][]string{
		// 通用对话：均衡的综合能力，适合日常聊天、信息咨询等
		// 推荐依据：MT-Bench 评分、综合性价比
		TaskTypeGeneral: {
			"claude",   // Claude 3.5 Sonnet - 综合能力强，多语言支持好
			"openai",   // GPT-4o - 通用能力均衡，工具调用出色
			"glm",      // GLM-4 - 国内首选，中文理解优秀
			"qwen",     // Qwen 2.0 - 性价比高，上下文窗口大
			"gemini",   // Gemini 1.5 Flash - 速度快，成本低
		},

		// 代码任务：代码生成、代码审查、调试、算法实现
		// 推荐依据：HumanEval、MBPP、CodeSearchNet 基准测试
		TaskTypeCode: {
			"claude",   // Claude 3.5 Sonnet - 代码能力顶级，支持 200K 上下文
			"openai",   // GPT-4o - 代码生成质量高，工具调用集成好
			"glm",      // GLM-4 Code - 国内代码能力最强
			"qwen",     // Qwen 2.0 Code - 长上下文代码理解
		},

		// 推理任务：数学问题、逻辑推理、复杂分析、决策支持
		// 推荐依据：GSM8K、MATH、BBH、ARC 基准测试
		TaskTypeReasoning: {
			"claude",   // Claude 3.5 Sonnet - 复杂推理能力领先
			"openai",   // GPT-4o - 逻辑推理精准
			"gemini",   // Gemini 1.5 Pro - 多模态推理强
			"glm",      // GLM-4 - 国内推理能力最优
			"qwen",     // Qwen 2.0 - 长上下文推理支持
		},

		// 中文内容：中文理解、生成、翻译、文化知识
		// 推荐依据：C-Eval、CMMLU、中文 MT-Bench
		TaskTypeChinese: {
			"glm",      // GLM-4 - 中文语义理解顶级，文化适配最佳
			"qwen",     // Qwen 2.0 - 中文生成流畅，性价比高
			"claude",   // Claude 3.5 Sonnet - 多语言支持，中文能力强
			"openai",   // GPT-4o - 中文理解准确
		},

		// 长文本处理：文档摘要、长文档问答、法律合同分析
		// 推荐依据：LongBench、WikiHop 长上下文基准
		TaskTypeLongText: {
			"claude",   // Claude 3.5 Sonnet - 原生支持 200K token，长文本理解最优
			"gemini",   // Gemini 1.5 Pro - 支持 1M+ token，超长上下文
			"qwen",     // Qwen 2.0 - 支持 128K+ token
			"glm",      // GLM-4 - 支持长上下文处理
			"openai",   // GPT-4o - 支持 128K token
		},
	}
}