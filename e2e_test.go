package main

import (
	"archive/zip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cfpaDirPackName = "CFPA-pack"

// fixtureMods: 合成整合包 mods 目录（含 fabric / forge / 无元数据 / disabled）。
var fixtureMods = map[string]map[string]string{
	"jei-fabric-1.0.jar": {
		"fabric.mod.json": `{"schemaVersion":1,"id":"jei","version":"1.0","provides":[{"id":"jeilib","version":"*"}]}`,
	},
	"sophisticatedbackpacks-3.20.1.jar": {
		"META-INF/mods.toml": "modLoader=\"javafml\"\nloaderVersion=\"[47,)\"\n[[mods]]\nmodId=\"sophisticatedbackpacks\"\nversion=\"3.20.1\"\n",
	},
	"mysterymod-1.0.jar": { // 无元数据 → uncertain，但文件名能匹配 cfpa "mysterymod"
		"random.txt": "hi",
	},
	"weirdarchive.jar": { // 无元数据 → uncertain，需 LLM 判断
		"data/":     "",
		"stuff.bin": "x",
	},
	"disabledmod-1.0.jar.disabled": { // 应被跳过
		"fabric.mod.json": `{"schemaVersion":1,"id":"disabledmod"}`,
	},
}

// fixtureCfpaMods: 合成 CFPA 包的翻译目录。
// minecraft=固定保留, jei/sophisticatedbackpacks=自动匹配, mysterymod=文件名匹配,
// unknownmod=需 LLM 保留, oldmod=应被删除。
var fixtureCfpaMods = []string{"minecraft", "jei", "sophisticatedbackpacks", "mysterymod", "unknownmod", "oldmod"}

func buildFixture(t *testing.T, zipPack bool) string {
	t.Helper()
	root := t.TempDir()
	for name, files := range fixtureMods {
		writeJar(t, filepath.Join(root, "mods", name), files)
	}
	if zipPack {
		zipPath := filepath.Join(root, "resourcepacks", cfpaDirPackName+".zip")
		if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
			t.Fatal(err)
		}
		zf, err := os.Create(zipPath)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(zf)
		for _, e := range append([]string{"pack.mcmeta"}, modLangEntries(fixtureCfpaMods)...) {
			w, err := zw.Create(e)
			if err != nil {
				t.Fatal(err)
			}
			w.Write([]byte(`{}`))
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		zf.Close()
	} else {
		mkPackDir(t, filepath.Join(root, "resourcepacks", cfpaDirPackName), fixtureCfpaMods)
	}
	return root
}

func modLangEntries(modids []string) []string {
	var es []string
	for _, id := range modids {
		es = append(es, "assets/"+id+"/lang/zh_cn.json")
	}
	return es
}

// startMockLLM 启动模拟 LLM API 并配置环境变量（测试不落盘、不交互）。
func startMockLLM(t *testing.T, reply string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("LLM_BASE_URL", srv.URL)
	t.Setenv("LLM_API_KEY", "test-key")
	t.Setenv("LLM_MODEL", "test-model")
}

const llmReply = `{"choices":[{"message":{"content":"{\"jar_mappings\":{\"weirdarchive.jar\":null},\"extra_keeps\":[\"unknownmod\"]}"}}]}`

func assertAssets(t *testing.T, root string, want map[string]bool) {
	t.Helper()
	base := filepath.Join(root, "resourcepacks", cfpaDirPackName, "assets")
	for id, exists := range want {
		if got := dirExists(filepath.Join(base, id)); got != exists {
			t.Errorf("assets/%s 存在=%v, want %v", id, got, exists)
		}
	}
}

func findReport(t *testing.T, root string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, "i18n_prune_report_*.md"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("未找到报告: %v %v", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestE2EWithLLM(t *testing.T) {
	root := buildFixture(t, false)
	startMockLLM(t, llmReply)
	opts := &Options{Root: root, Keep: defaultKeepSet(), Yes: true, UseLLM: true}
	if err := run(opts, nil); err != nil {
		t.Fatal(err)
	}
	assertAssets(t, root, map[string]bool{
		"minecraft": true, "jei": true, "sophisticatedbackpacks": true, "mysterymod": true, "unknownmod": true,
		"oldmod": false,
	})
	report := findReport(t, root)
	for _, want := range []string{"oldmod", "unknownmod", "LLM 判定保留", "weirdarchive"} {
		if !strings.Contains(report, want) {
			t.Errorf("报告缺少 %q", want)
		}
	}
}

func TestE2EDryRun(t *testing.T) {
	root := buildFixture(t, false)
	startMockLLM(t, llmReply)
	opts := &Options{Root: root, Keep: defaultKeepSet(), DryRun: true, Yes: true, UseLLM: true}
	if err := run(opts, nil); err != nil {
		t.Fatal(err)
	}
	// dry-run 不删任何东西
	assertAssets(t, root, map[string]bool{"oldmod": true, "unknownmod": true, "minecraft": true})
	if report := findReport(t, root); !strings.Contains(report, "dry-run") {
		t.Error("报告应标注 dry-run")
	}
}

// TestE2ENoLLMByDefault: 默认不启用 LLM，未匹配的一律删除（unknownmod 也被删除）。
func TestE2ENoLLMByDefault(t *testing.T) {
	root := buildFixture(t, false)
	opts := &Options{Root: root, Keep: defaultKeepSet(), Yes: true}
	if err := run(opts, nil); err != nil {
		t.Fatal(err)
	}
	// 跳过 LLM → unknownmod 也被删除；mysterymod 靠文件名保留
	assertAssets(t, root, map[string]bool{
		"minecraft": true, "jei": true, "sophisticatedbackpacks": true, "mysterymod": true,
		"unknownmod": false, "oldmod": false,
	})
}

func TestE2EApplyDecisions(t *testing.T) {
	root := buildFixture(t, false)
	decPath := filepath.Join(root, "decisions.json")
	if err := os.WriteFile(decPath, []byte(`{"jar_mappings":{"weirdarchive.jar":null},"extra_keeps":["unknownmod"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := &Options{Root: root, Keep: defaultKeepSet(), ApplyDecisions: decPath, Yes: true}
	if err := run(opts, nil); err != nil {
		t.Fatal(err)
	}
	assertAssets(t, root, map[string]bool{"unknownmod": true, "oldmod": false})
}

func TestE2EZipPack(t *testing.T) {
	root := buildFixture(t, true)
	opts := &Options{Root: root, Keep: defaultKeepSet(), Yes: true}
	if err := run(opts, nil); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(root, "resourcepacks", cfpaDirPackName+".zip")
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	// 原 7 条（pack.mcmeta + 6 mods），删除 unknownmod/oldmod 后剩 5 条
	if len(zr.File) != 5 {
		t.Fatalf("条目数 = %d, want 5", len(zr.File))
	}
	for _, want := range []string{"pack.mcmeta", "assets/minecraft/lang/zh_cn.json", "assets/jei/lang/zh_cn.json", "assets/mysterymod/lang/zh_cn.json"} {
		if !names[want] {
			t.Errorf("缺少 %s", want)
		}
	}
	for _, bad := range []string{"assets/unknownmod", "assets/oldmod"} {
		if names[bad+"/lang/zh_cn.json"] {
			t.Errorf("不应保留 %s", bad)
		}
	}
}

func TestE2EVerbose(t *testing.T) {
	root := buildFixture(t, false)
	startMockLLM(t, llmReply)
	opts := &Options{Root: root, Keep: defaultKeepSet(), Yes: true, Verbose: true, UseLLM: true}
	if err := run(opts, nil); err != nil {
		t.Fatal(err)
	}
	// verbose 不影响结果，只增加输出
	assertAssets(t, root, map[string]bool{
		"minecraft": true, "jei": true, "sophisticatedbackpacks": true, "mysterymod": true, "unknownmod": true,
		"oldmod": false,
	})
}

func TestE2EReportReflectsMatchKinds(t *testing.T) {
	root := buildFixture(t, false)
	startMockLLM(t, llmReply)
	opts := &Options{Root: root, Keep: defaultKeepSet(), Yes: true, UseLLM: true}
	if err := run(opts, nil); err != nil {
		t.Fatal(err)
	}
	report := findReport(t, root)
	for _, want := range []string{"固定保留", "自动匹配", "文件名匹配", "LLM 判定明细", "无法解析 modid 的 jar", "mysterymod-1.0.jar"} {
		if !strings.Contains(report, want) {
			t.Errorf("报告缺少 %q", want)
		}
	}
}
