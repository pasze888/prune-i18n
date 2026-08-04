package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkPackDir(t *testing.T, root string, modids []string) {
	t.Helper()
	for _, id := range modids {
		lang := filepath.Join(root, "assets", id, "lang")
		if err := os.MkdirAll(lang, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(lang, "zh_cn.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "pack.mcmeta"), []byte(`{"pack":{"pack_format":15}}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectPacks(t *testing.T) {
	respDir := t.TempDir()
	mkPackDir(t, filepath.Join(respDir, "CFPA-pack"), []string{"minecraft", "jei", "create"})
	// zip 型资源包
	zipPath := filepath.Join(respDir, "pack2.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	for _, name := range []string{"pack.mcmeta", "assets/foo/lang/zh_cn.json", "assets/bar/lang/zh_cn.lang"} {
		w, _ := zw.Create(name)
		w.Write([]byte("x"))
	}
	zw.Close()
	zf.Close()
	// 无效条目：无 pack.mcmeta 的目录、普通文件
	os.MkdirAll(filepath.Join(respDir, "not-a-pack"), 0o755)
	os.WriteFile(filepath.Join(respDir, "notes.txt"), []byte("hi"), 0o644)

	packs, err := detectPacks(respDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 2 {
		t.Fatalf("len(packs) = %d, want 2: %+v", len(packs), packs)
	}
	if packs[0].Name != "CFPA-pack" || packs[0].ModCount != 3 || packs[0].IsZip {
		t.Errorf("pack1 unexpected: %+v", packs[0])
	}
	if packs[1].Name != "pack2.zip" || packs[1].ModCount != 2 || !packs[1].IsZip {
		t.Errorf("pack2 unexpected: %+v", packs[1])
	}
}

func TestOpenPack(t *testing.T) {
	dir := t.TempDir()
	mkPackDir(t, filepath.Join(dir, "p"), []string{"minecraft"})
	if _, err := openPack(filepath.Join(dir, "p")); err != nil {
		t.Fatalf("openPack dir: %v", err)
	}
	if _, err := openPack(filepath.Join(dir, "no-such")); err == nil {
		t.Fatal("不存在路径应报错")
	}
	os.MkdirAll(filepath.Join(dir, "empty"), 0o755)
	if _, err := openPack(filepath.Join(dir, "empty")); err == nil {
		t.Fatal("无 pack.mcmeta 目录应报错")
	}
}

func TestLoadCfpaMods(t *testing.T) {
	dir := t.TempDir()
	mkPackDir(t, filepath.Join(dir, "p"), []string{"Minecraft", "Jei", "create"})
	pack := Pack{Name: "p", Path: filepath.Join(dir, "p")}
	mods, err := loadCfpaMods(pack)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"minecraft", "jei", "create"} {
		if !mods[want] {
			t.Errorf("缺少 modid %q: %v", want, mods)
		}
	}
	// zip 型
	zipPath := filepath.Join(dir, "p.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	for _, name := range []string{"pack.mcmeta", "assets/foo/lang/zh_cn.json", "assets/foo/lang/en_us.json", "assets/bar/lang/zh_cn.lang"} {
		w, _ := zw.Create(name)
		w.Write([]byte("x"))
	}
	zw.Close()
	zf.Close()
	mods, err = loadCfpaMods(Pack{Name: "p.zip", Path: zipPath, IsZip: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) != 2 || !mods["foo"] || !mods["bar"] {
		t.Errorf("zip 型 mods = %v", mods)
	}
}

func TestLoadCfpaModsEmpty(t *testing.T) {
	dir := t.TempDir()
	mkPackDir(t, filepath.Join(dir, "p"), []string{"minecraft"})
	os.RemoveAll(filepath.Join(dir, "p", "assets"))
	if _, err := loadCfpaMods(Pack{Name: "p", Path: filepath.Join(dir, "p")}); err == nil {
		t.Fatal("无翻译目录应报错")
	}
}

func TestRewriteZipExcluding(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "pack.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	for _, name := range []string{
		"pack.mcmeta",
		"assets/a/lang/zh_cn.json",
		"assets/b/lang/zh_cn.json",
		"assets/a/textures/icon.png",
	} {
		w, _ := zw.Create(name)
		w.Write([]byte("x"))
	}
	zw.Close()
	zf.Close()

	removed, err := rewriteZipExcluding(zipPath, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if len(zr.File) != 2 {
		t.Fatalf("剩余条目 %d, want 2", len(zr.File))
	}
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "assets/a") {
			t.Errorf("不应保留 %s", f.Name)
		}
	}
	// 无匹配时不应动原文件
	removed, err = rewriteZipExcluding(zipPath, []string{"zzz"})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
}
