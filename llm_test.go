package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseDecision(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantJar  string // jar → id 的代表性键值对，用 "jar=id" 描述
		wantKeep []string
		wantErr  bool
	}{
		{
			name:     "plain",
			content:  `{"jar_mappings":{"a.jar":"alpha"},"extra_keeps":["beta"]}`,
			wantJar:  "a.jar=alpha",
			wantKeep: []string{"beta"},
		},
		{
			name:     "fenced and padded",
			content:  "好的，分析如下：\n```json\n{\"jar_mappings\": {\"a.jar\": null}, \"extra_keeps\": [\"BETA\"]}\n```\n完毕",
			wantJar:  "a.jar=",
			wantKeep: []string{"beta"},
		},
		{
			name:    "no json",
			content: "抱歉我无法判断",
			wantErr: true,
		},
		{
			name:    "bad value type",
			content: `{"jar_mappings":{"a.jar":123}}`,
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dec, err := parseDecision(c.content)
			if c.wantErr {
				if err == nil {
					t.Fatal("应返回错误")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			jar, id, ok := strings.Cut(c.wantJar, "=")
			if !ok {
				t.Fatal("bad test spec")
			}
			if got, exists := dec.JarMappings[jar]; !exists || got != id {
				t.Errorf("JarMappings[%q] = %q (exists=%v), want %q", jar, got, exists, id)
			}
			if strings.Join(dec.ExtraKeeps, ",") != strings.Join(c.wantKeep, ",") {
				t.Errorf("ExtraKeeps = %v, want %v", dec.ExtraKeeps, c.wantKeep)
			}
		})
	}
}

func TestApplyDecision(t *testing.T) {
	candidates := map[string]bool{"alpha": true, "beta": true, "gamma": true}
	cfpaMods := map[string]bool{"alpha": true, "beta": true, "gamma": true, "delta": true}
	jars := []JarInfo{{Name: "a.jar"}, {Name: "b.jar"}}
	dec := Decision{
		JarMappings: map[string]string{
			"a.jar": "notexist", // jar 存在但 modid 不在 CFPA → 警告
			"b.jar": "",         // null → 无对应
			"c.jar": "beta",     // jar 不在列表 → 警告，beta 仍为候选
		},
		ExtraKeeps: []string{"gamma", "notexist"},
	}
	kept, notes := applyDecision(dec, candidates, cfpaMods, jars)
	if !candidates["alpha"] || !candidates["beta"] {
		t.Errorf("候选不应被移除: alpha=%v beta=%v", candidates["alpha"], candidates["beta"])
	}
	if candidates["gamma"] {
		t.Error("gamma 应被 extra_keeps 保留并移出候选")
	}
	if len(kept) != 1 || kept[0] != "gamma" {
		t.Errorf("kept = %v, want [gamma]", kept)
	}
	warnCount := 0
	for _, n := range notes {
		if strings.Contains(n, "不在整合包") || strings.Contains(n, "不在 CFPA 包中") {
			warnCount++
		}
	}
	if warnCount != 3 {
		t.Errorf("应有 3 条警告（c.jar 不在列表、a.jar 映射的 modid 不在 CFPA、extra_keeps notexist 不在 CFPA）, notes = %v", notes)
	}
}

func TestCallLLM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Model != "test-model" {
			t.Errorf("model = %q", body.Model)
		}
		if len(body.Messages) != 1 || !strings.Contains(body.Messages[0].Content, "删除候选") {
			t.Errorf("prompt 内容异常: %q", body.Messages[0].Content)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"jar_mappings\":{}}"}}]}`)
	}))
	defer srv.Close()

	cfg := LLMConfig{BaseURL: srv.URL, APIKey: "test-key", Model: "test-model"}
	reply, err := callLLM(cfg, buildPrompt([]string{"alpha"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "jar_mappings") {
		t.Errorf("reply = %q", reply)
	}
}

func TestCallLLMError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid key"}}`)
	}))
	defer srv.Close()
	_, err := callLLM(LLMConfig{BaseURL: srv.URL, APIKey: "bad", Model: "m"}, "x")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("应返回 401 错误, got %v", err)
	}
}

func TestBuildPrompt(t *testing.T) {
	p := buildPrompt([]string{"alpha", "beta"}, []JarInfo{{Name: "mystery.jar", Size: 123, Entries: []string{"META-INF/", "assets/"}}})
	for _, want := range []string{"alpha", "beta", "mystery.jar", "jar_mappings", "extra_keeps"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt 缺少 %q", want)
		}
	}
}
