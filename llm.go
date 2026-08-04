package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// LLMConfig 保存 LLM API 配置，仅存在于内存中，不写入任何文件。
type LLMConfig struct {
	BaseURL string
	APIKey  string
	Model   string
}

// Decision 是 LLM 返回的决策：jar → CFPA modid 的映射（空串表示无对应），以及应额外保留的 modid。
type Decision struct {
	JarMappings map[string]string
	ExtraKeeps  []string
}

const llmTimeout = 5 * time.Minute

// resolveLLMConfig 优先使用环境变量（不落盘），否则走交互输入。
func resolveLLMConfig(ask func() (LLMConfig, error)) (LLMConfig, error) {
	cfg := LLMConfig{
		BaseURL: envOr("LLM_BASE_URL", "https://api.openai.com/v1"),
		APIKey:  os.Getenv("LLM_API_KEY"),
		Model:   envOr("LLM_MODEL", "gpt-4o-mini"),
	}
	if cfg.APIKey != "" {
		return cfg, nil
	}
	if ask != nil {
		return ask()
	}
	return cfg, fmt.Errorf("未配置 LLM API Key（可设置环境变量 LLM_API_KEY，或交互输入）")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// buildPrompt 生成给 LLM 的中文提示词：删除候选 modid + 无法解析的 jar（含内容摘要）。
func buildPrompt(candidates []string, uncertain []JarInfo) string {
	var b strings.Builder
	b.WriteString("你是 Minecraft 模组识别助手。以下是 CFPA 汉化资源包（Minecraft-Mod-Language-Modpack）与整合包的对照结果，请帮助判断。\n\n")
	fmt.Fprintf(&b, "一、CFPA 汉化资源包中「未被整合包任何 mod 匹配到」的 modid（即删除候选，共 %d 个）：\n", len(candidates))
	for _, id := range candidates {
		b.WriteString("- " + id + "\n")
	}
	b.WriteString("\n二、整合包中「无法自动解析出 modid」的 jar 文件（括号内为文件大小与解析情况，其后为 jar 内顶层内容清单）：\n")
	if len(uncertain) == 0 {
		b.WriteString("（无）\n")
	}
	for _, j := range uncertain {
		extra := ""
		if j.ParseErr != "" {
			extra = "；" + j.ParseErr
		}
		fmt.Fprintf(&b, "## %s（%d 字节%s）\n", j.Name, j.Size, extra)
		for _, e := range j.Entries {
			b.WriteString("- " + e + "\n")
		}
	}
	b.WriteString("\n请只输出一个 JSON 对象（不要输出任何其他文字、解释或 markdown 代码块标记），格式：\n")
	b.WriteString(`{"jar_mappings": {"<jar文件名>": "<modid>" 或 null, ...}, "extra_keeps": ["<modid>", ...]}` + "\n\n")
	b.WriteString("要求：\n")
	b.WriteString("1. jar_mappings 的 key 必须与「二」中给出的 jar 文件名完全一致，一个文件一条；判断该 jar 对应删除候选中的哪个 modid；若无法判断或该 jar 不是模组，值为 null。\n")
	b.WriteString("2. jar_mappings 与 extra_keeps 中的 modid 只能来自「一」的删除候选列表。\n")
	b.WriteString("3. extra_keeps 用于：某个候选 modid 实际属于整合包（例如模组整合了多个 modid、或汉化目录名与 jar 文件名不同），应保留。\n")
	b.WriteString("4. 删除候选中的其余 modid 会被永久删除，请谨慎判断。\n")
	return b.String()
}

// callLLM 调用 OpenAI 兼容的 /chat/completions 接口。
func callLLM(cfg LLMConfig, prompt string) (string, error) {
	url := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
	payload := map[string]any{
		"model":       cfg.Model,
		"messages":    []map[string]string{{"role": "user", "content": prompt}},
		"temperature": 0,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	client := &http.Client{Timeout: llmTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM API 返回状态 %d: %s", resp.StatusCode, truncate(string(body), 500))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("解析 LLM 响应失败: %v", err)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("LLM 响应中没有 choices")
	}
	return out.Choices[0].Message.Content, nil
}

type rawDecision struct {
	JarMappings map[string]json.RawMessage `json:"jar_mappings"`
	ExtraKeeps  []string                   `json:"extra_keeps"`
}

// parseDecision 宽松解析 LLM 返回的 JSON（容忍 ```json 围栏与前后多余文字）。
func parseDecision(content string) (Decision, error) {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	start, end := strings.IndexByte(content, '{'), strings.LastIndexByte(content, '}')
	if start < 0 || end <= start {
		return Decision{}, fmt.Errorf("回复中未找到 JSON 对象")
	}
	content = content[start : end+1]
	var raw rawDecision
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return Decision{}, err
	}
	dec := Decision{JarMappings: map[string]string{}, ExtraKeeps: []string{}}
	for jar, v := range raw.JarMappings {
		if len(v) == 0 || string(v) == "null" {
			dec.JarMappings[jar] = ""
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return Decision{}, fmt.Errorf("jar_mappings[%s] 的值不是字符串或 null", jar)
		}
		dec.JarMappings[jar] = strings.ToLower(strings.TrimSpace(s))
	}
	for _, id := range raw.ExtraKeeps {
		id = strings.ToLower(strings.TrimSpace(id))
		if id != "" {
			dec.ExtraKeeps = append(dec.ExtraKeeps, id)
		}
	}
	return dec, nil
}

// loadDecisionFile 读取之前生成的决策 JSON 文件。
func loadDecisionFile(path string) (Decision, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Decision{}, err
	}
	return parseDecision(string(data))
}

// applyDecision 将 LLM 决策应用到删除候选集：映射成功/额外保留的 modid 移出候选。
func applyDecision(dec Decision, candidates map[string]bool, cfpaMods map[string]bool, jars []JarInfo) (kept []string, notes []string) {
	keptSet := map[string]bool{}
	jarByName := map[string]bool{}
	for _, j := range jars {
		jarByName[strings.ToLower(j.Name)] = true
	}
	keep := func(id, why string) {
		if candidates[id] {
			delete(candidates, id)
			keptSet[id] = true
			notes = append(notes, "保留: "+id+"（"+why+"）")
		}
	}
	for jar, id := range dec.JarMappings {
		if !jarByName[strings.ToLower(strings.TrimSpace(jar))] {
			notes = append(notes, "警告: 决策中的 jar 不在整合包 mods 列表中: "+jar)
			continue
		}
		if id == "" {
			continue
		}
		if !cfpaMods[id] {
			notes = append(notes, "警告: 决策映射的 modid 不在 CFPA 包中: "+jar+" → "+id)
			continue
		}
		keep(id, "LLM 判定 jar "+jar+" 对应")
	}
	for _, id := range dec.ExtraKeeps {
		if !cfpaMods[id] {
			notes = append(notes, "警告: extra_keeps 中的 modid 不在 CFPA 包中: "+id)
			continue
		}
		keep(id, "LLM 判定应保留")
	}
	for id := range keptSet {
		kept = append(kept, id)
	}
	sort.Strings(kept)
	return kept, notes
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
