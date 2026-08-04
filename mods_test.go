package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeJar 在指定路径生成一个内容为 files（name → content）的 zip/jar。
func writeJar(t *testing.T, path string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func TestParseJarFabric(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jei.jar")
	writeJar(t, path, map[string]string{
		"fabric.mod.json": `{"schemaVersion":1,"id":"jei","version":"1.0","provides":[{"id":"jeilib","version":"*"}]}`,
	})
	j, err := parseJar(path)
	if err != nil {
		t.Fatal(err)
	}
	if j.Uncertain {
		t.Fatal("fabric jar 不应 uncertain")
	}
	if got := strings.Join(j.ModIDs, ","); got != "jei" {
		t.Errorf("ModIDs = %q, want jei", got)
	}
	if got := strings.Join(j.Provided, ","); got != "jeilib" {
		t.Errorf("Provided = %q, want jeilib", got)
	}
}

func TestParseJarForge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soph.jar")
	writeJar(t, path, map[string]string{
		"META-INF/mods.toml": "modLoader=\"javafml\"\nloaderVersion=\"[47,)\"\n[[mods]]\nmodId=\"sophisticatedbackpacks\"\nversion=\"3.20.1\"\n[[mods]]\nmodId=\"sophbackpacks\"\nversion=\"3.20.1\"\n",
	})
	j, err := parseJar(path)
	if err != nil {
		t.Fatal(err)
	}
	if j.Uncertain {
		t.Fatal("forge jar 不应 uncertain")
	}
	if got := strings.Join(j.ModIDs, ","); got != "sophbackpacks,sophisticatedbackpacks" {
		t.Errorf("ModIDs = %q", got)
	}
}

func TestParseJarNeoForge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "neof.jar")
	writeJar(t, path, map[string]string{
		"META-INF/neoforge.mods.toml": "[[mods]]\nmodId=\"neoforgeexample\"\nversion=\"1.0\"\n",
	})
	j, err := parseJar(path)
	if err != nil {
		t.Fatal(err)
	}
	if j.Uncertain || len(j.ModIDs) != 1 || j.ModIDs[0] != "neoforgeexample" {
		t.Fatalf("unexpected: %+v", j)
	}
}

func TestParseJarQuilt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quilt.jar")
	writeJar(t, path, map[string]string{
		"quilt.mod.json": `{"quilt_loader":{"id":"quiltmod"}}`,
	})
	j, err := parseJar(path)
	if err != nil {
		t.Fatal(err)
	}
	if j.Uncertain || len(j.ModIDs) != 1 || j.ModIDs[0] != "quiltmod" {
		t.Fatalf("unexpected: %+v", j)
	}
}

func TestParseJarMcmodInfo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jar")
	writeJar(t, path, map[string]string{
		"mcmod.info": `[{"modid":"legacymod","name":"Legacy"}]`,
	})
	j, err := parseJar(path)
	if err != nil {
		t.Fatal(err)
	}
	if j.Uncertain || len(j.ModIDs) != 1 || j.ModIDs[0] != "legacymod" {
		t.Fatalf("unexpected: %+v", j)
	}
}

func TestParseJarUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mystery.jar")
	writeJar(t, path, map[string]string{
		"data/":                "",
		"stuff.bin":            "x",
		"nested/deep.txt":      "y",
		"META-INF/MANIFEST.MF": "Manifest-Version: 1.0\n",
	})
	j, err := parseJar(path)
	if err != nil {
		t.Fatal(err)
	}
	if !j.Uncertain {
		t.Fatal("无元数据 jar 应 uncertain")
	}
	// 顶层条目应去重并包含 data、stuff.bin、nested、META-INF
	got := strings.Join(j.Entries, ",")
	for _, want := range []string{"data", "stuff.bin", "nested", "META-INF"} {
		if !strings.Contains(got, want) {
			t.Errorf("Entries 缺少 %q: %s", want, got)
		}
	}
}

func TestParseJarCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.jar")
	if err := os.WriteFile(path, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	j, err := parseJar(path)
	if err == nil {
		t.Fatal("损坏 jar 应返回错误")
	}
	if !j.Uncertain || j.ParseErr == "" {
		t.Fatalf("unexpected: %+v", j)
	}
}

func TestExtractModIDs(t *testing.T) {
	cases := []struct {
		name string
		toml string
		want []string
	}{
		{"basic", "modLoader=\"javafml\"\n[[mods]]\nmodId=\"alpha\"\n", []string{"alpha"}},
		{"multi", "[[mods]]\nmodId=\"alpha\"\n[[mods]]\nmodId=\"beta\"\n", []string{"alpha", "beta"}},
		{"inline comment", "[[mods]]\nmodId=\"alpha\" # 主模组\n", []string{"alpha"}},
		{"comment line", "# modId=\"fake\"\n[[mods]]\nmodId=\"alpha\"\n", []string{"alpha"}},
		{"case and spaces", "[[mods]]\n  modID   =  \"ALPHA\"\n", []string{"alpha"}},
		{"single quote", "[[mods]]\nmodId='alpha'\n", []string{"alpha"}},
		{"duplicate", "[[mods]]\nmodId=\"alpha\"\n[[mods]]\nmodId=\"alpha\"\n", []string{"alpha"}},
		{"none", "modLoader=\"javafml\"\n", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractModIDs(c.toml)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("extractModIDs = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMatchesJarName(t *testing.T) {
	jars := []JarInfo{
		{Name: "jei-1.20.1-forge.jar"},
		{Name: "sophisticatedbackpacks-3.20.1.jar"},
		{Name: "create-0.5.1.jar"},
	}
	cases := []struct {
		modid string
		want  bool
	}{
		{"jei", true},
		{"sophisticatedbackpacks", true},
		{"create", true},
		{"createaddition", false}, // 片段必须完整相等，避免误判
		{"forge", false},          // forge 是 jar 名片段但不是独立 mod
		{"1.20.1", false},         // 版本号不应匹配
	}
	for _, c := range cases {
		if got := matchesJarName(c.modid, jars); got != c.want {
			t.Errorf("matchesJarName(%q) = %v, want %v", c.modid, got, c.want)
		}
	}
}

func TestScanModsSkipsDisabled(t *testing.T) {
	dir := t.TempDir()
	writeJar(t, filepath.Join(dir, "a.jar"), map[string]string{"fabric.mod.json": `{"schemaVersion":1,"id":"a"}`})
	writeJar(t, filepath.Join(dir, "b.jar.disabled"), map[string]string{"fabric.mod.json": `{"schemaVersion":1,"id":"b"}`})
	writeJar(t, filepath.Join(dir, "sub", "c.jar"), map[string]string{"fabric.mod.json": `{"schemaVersion":1,"id":"c"}`})
	jars := scanMods(dir)
	if len(jars) != 2 {
		t.Fatalf("len(jars) = %d, want 2 (disabled 应被跳过): %+v", len(jars), jars)
	}
	if jars[0].Name != "a.jar" || jars[1].Name != "c.jar" {
		t.Errorf("unexpected order: %v, %v", jars[0].Name, jars[1].Name)
	}
}
