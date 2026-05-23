package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"final/config"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// Message 表示单条对话消息
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Client 是 LLM 聊天客户端
type Client struct {
	cfg        *config.APIConfig
	httpClient *http.Client
}

// New 创建 LLM 客户端
func New(cfg *config.APIConfig) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// Chat 发送对话请求，返回回复文本。
// 若配置了真实 API Key 则调用远程接口，否则使用 Mock。
func (c *Client) Chat(systemPrompt string, messages []Message) string {
	return c.ChatContext(context.Background(), systemPrompt, messages)
}

// ChatContext 带 context 的对话请求，支持取消。
func (c *Client) ChatContext(ctx context.Context, systemPrompt string, messages []Message) string {
	if c.cfg.IsRealLLM() {
		reply, err := c.callAPIWithContext(ctx, systemPrompt, messages)
		if err != nil {
			if ctx.Err() != nil {
				return "[已中断]"
			}
			log.Printf("LLM API 调用失败: %v，回退到 Mock", err)
			return c.mock(messages)
		}
		return reply
	}
	return c.mock(messages)
}

// ── OpenAI 兼容接口调用 ──────────────────────────────────────────────────

type apiRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
}

type apiResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *Client) callAPI(systemPrompt string, messages []Message) (string, error) {
	return c.callAPIWithContext(context.Background(), systemPrompt, messages)
}

func (c *Client) callAPIWithContext(ctx context.Context, systemPrompt string, messages []Message) (string, error) {
	var msgs []Message
	if systemPrompt != "" {
		msgs = append(msgs, Message{Role: "system", Content: systemPrompt})
	}
	msgs = append(msgs, messages...)

	body, err := json.Marshal(apiRequest{
		Model:       c.cfg.LLMModel,
		Messages:    msgs,
		Temperature: c.cfg.Temperature,
	})
	if err != nil {
		return "", fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.LLMAPIUrl, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("构建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.LLMAPIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("HTTP 请求失败: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}

	var result apiResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("解析响应失败: %w, body: %s", err, string(data))
	}
	if result.Error != nil {
		return "", fmt.Errorf("API 错误: %s", result.Error.Message)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("API 返回空结果, body: %s", string(data))
	}
	return result.Choices[0].Message.Content, nil
}

// ── Embedding API ──────────────────────────────────────────────────────

type embedRequest struct {
	Model string      `json:"model"`
	Input interface{} `json:"input"` // string for text API, []map for multimodal API
}

// embedResponse 标准文本 embedding 响应（data 为数组）
type embedResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// multimodalEmbedResponse 多模态 embedding 响应（data 为单个对象）
type multimodalEmbedResponse struct {
	Data struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Embed 调用 Embedding API 将文本转为向量；失败时返回 nil
// 自动检测多模态 embedding 端点并适配请求格式
func (c *Client) Embed(text string) ([]float64, error) {
	return c.EmbedContext(context.Background(), text)
}

// EmbedContext 带 context 的 Embedding 请求，支持取消
func (c *Client) EmbedContext(ctx context.Context, text string) ([]float64, error) {
	if c.cfg.EmbeddingAPIUrl == "" || c.cfg.EmbeddingAPIKey == "" {
		return nil, fmt.Errorf("embedding API 未配置")
	}

	// 多模态 embedding 使用 /multimodal_embeddings 端点，input 为结构化数组
	var input interface{}
	apiURL := c.cfg.EmbeddingAPIUrl
	if strings.Contains(apiURL, "/embeddings/multimodal") {
		input = []map[string]string{{"type": "text", "text": text}}
	} else {
		input = text
	}

	body, err := json.Marshal(embedRequest{Model: c.cfg.EmbeddingModel, Input: input})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.EmbeddingAPIKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding API 返回错误状态 %d, body: %s", resp.StatusCode, string(data))
	}
	isMultimodal := strings.Contains(apiURL, "/embeddings/multimodal")
	var embedding []float64
	if isMultimodal {
		var result multimodalEmbedResponse
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("解析 embedding 响应失败: %w, body: %s", err, string(data))
		}
		if result.Error != nil {
			return nil, fmt.Errorf("embedding API 错误: %s", result.Error.Message)
		}
		embedding = result.Data.Embedding
	} else {
		var result embedResponse
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("解析 embedding 响应失败: %w, body: %s", err, string(data))
		}
		if result.Error != nil {
			return nil, fmt.Errorf("embedding API 错误: %s", result.Error.Message)
		}
		if len(result.Data) == 0 {
			return nil, fmt.Errorf("embedding 返回空结果")
		}
		embedding = result.Data[0].Embedding
	}
	if len(embedding) == 0 {
		return nil, fmt.Errorf("embedding 返回空向量")
	}
	return embedding, nil
}

// ── LLM-based Preference Extraction ────────────────────────────────────

// ExtractPreferences 用 LLM 从用户消息中提取偏好键值对。
// 返回 map[key]value；提取失败或无偏好时返回空 map。
func (c *Client) ExtractPreferences(msg string) map[string]string {
	if !c.cfg.IsRealLLM() {
		return extractRuleBased(msg)
	}
	prompt := `从下面这句用户消息中，提取所有用户的个人信息和偏好，输出 JSON 对象（key为中文名称，value为具体值）。
如果没有任何偏好信息，输出 {}。
只输出 JSON，不要有其他内容。

消息：` + msg
	raw, err := c.callAPI("", []Message{{Role: "user", Content: prompt}})
	if err != nil {
		return extractRuleBased(msg)
	}
	raw = strings.TrimSpace(raw)
	// 去掉可能的 markdown 代码块包裹
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	var result map[string]string
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return extractRuleBased(msg)
	}
	return result
}

// extractRuleBased 规则兜底：无 API 时使用
func extractRuleBased(msg string) map[string]string {
	result := make(map[string]string)
	if strings.Contains(msg, "我喜欢") {
		parts := strings.SplitN(msg, "喜欢", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
			result["喜好"] = strings.TrimSpace(parts[1])
		}
	}
	if strings.Contains(msg, "我爱") {
		parts := strings.SplitN(msg, "爱", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
			result["喜好"] = strings.TrimSpace(parts[1])
		}
	}
	if strings.Contains(msg, "我叫") {
		parts := strings.SplitN(msg, "叫", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
			result["姓名"] = strings.TrimSpace(parts[1])
		}
	}
	return result
}

// ── Mock（无 API Key 时使用）────────────────────────────────────────────

func (c *Client) mock(messages []Message) string {
	var userQuery string
	for _, m := range messages {
		if m.Role == "user" {
			userQuery = m.Content
		}
	}
	q := strings.ToLower(userQuery)
	switch {
	case strings.Contains(q, "你是谁"):
		return "我是一个全能 AI 助手，具备知识库、工具调用、推理、记忆和稳定执行能力。"
	case strings.Contains(q, "后端工程师"):
		return "后端工程师负责服务器端逻辑开发：API 设计、数据库、业务逻辑、系统架构、性能优化。常用 Go / Java / Python / MySQL / Redis。"
	default:
		return fmt.Sprintf("收到：「%s」——这是模拟 LLM 回复，接入真实 API 后会更智能。", userQuery)
	}
}
