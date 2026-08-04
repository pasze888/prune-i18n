package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Pack 描述一个资源包（CFPA 翻译包）：文件夹或 zip 压缩包。
type Pack struct {
	Name     string
	Path     string
	IsZip    bool
	ModCount int // assets/<modid>/lang/ 翻译目录数量
}

var assetLangRe = regexp.MustCompile(`^assets/([^/]+)/lang/`)

func packType(p Pack) string {
	if p.IsZip {
		return "zip 压缩包"
	}
	return "文件夹"
}

// detectPacks 扫描 resourcepacks/ 下所有包含 pack.mcmeta 的资源包（文件夹或 zip）。
func detectPacks(respDir string) ([]Pack, error) {
	entries, err := os.ReadDir(respDir)
	if err != nil {
		return nil, err
	}
	var packs []Pack
	for _, e := range entries {
		p := filepath.Join(respDir, e.Name())
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(p, "pack.mcmeta")); err != nil {
				continue
			}
			packs = append(packs, Pack{Name: e.Name(), Path: p, ModCount: countModsDir(p)})
		} else if strings.EqualFold(filepath.Ext(e.Name()), ".zip") {
			n, ok, err := zipHasPackMcmeta(p)
			if err != nil || !ok {
				continue
			}
			packs = append(packs, Pack{Name: e.Name(), Path: p, IsZip: true, ModCount: n})
		}
	}
	sort.Slice(packs, func(i, k int) bool { return packs[i].Name < packs[k].Name })
	return packs, nil
}

// openPack 校验用户显式指定的资源包路径（文件夹或 zip）。
func openPack(path string) (Pack, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Pack{}, err
	}
	if fi.IsDir() {
		if _, err := os.Stat(filepath.Join(path, "pack.mcmeta")); err != nil {
			return Pack{}, fmt.Errorf("目录不是有效的资源包（缺少 pack.mcmeta）: %s", path)
		}
		return Pack{Name: filepath.Base(path), Path: path, ModCount: countModsDir(path)}, nil
	}
	n, ok, err := zipHasPackMcmeta(path)
	if err != nil {
		return Pack{}, err
	}
	if !ok {
		return Pack{}, fmt.Errorf("zip 不是有效的资源包（缺少 pack.mcmeta）: %s", path)
	}
	return Pack{Name: filepath.Base(path), Path: path, IsZip: true, ModCount: n}, nil
}

// countModsDir 统计目录型资源包中 assets/<modid>/lang/ 非空目录的数量。
func countModsDir(root string) int {
	ds, err := os.ReadDir(filepath.Join(root, "assets"))
	if err != nil {
		return 0
	}
	n := 0
	for _, d := range ds {
		if !d.IsDir() {
			continue
		}
		if fs, err := os.ReadDir(filepath.Join(root, "assets", d.Name(), "lang")); err == nil && len(fs) > 0 {
			n++
		}
	}
	return n
}

// zipHasPackMcmeta 检查 zip 是否为资源包（根目录含 pack.mcmeta），并统计翻译目录数量。
func zipHasPackMcmeta(path string) (int, bool, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return 0, false, err
	}
	defer zr.Close()
	hasMC := false
	count := 0
	seen := map[string]bool{}
	for _, f := range zr.File {
		if f.Name == "pack.mcmeta" {
			hasMC = true
		}
		if m := assetLangRe.FindStringSubmatch(f.Name); m != nil {
			if !seen[m[1]] {
				seen[m[1]] = true
				count++
			}
		}
	}
	return count, hasMC, nil
}

// loadCfpaMods 提取资源包中的全部模组翻译目录名（modid），统一小写。
func loadCfpaMods(pack Pack) (map[string]bool, error) {
	var mods map[string]bool
	var err error
	if pack.IsZip {
		mods, err = loadCfpaModsZip(pack.Path)
	} else {
		mods, err = loadCfpaModsDir(pack.Path)
	}
	if err != nil {
		return nil, err
	}
	if len(mods) == 0 {
		return nil, fmt.Errorf("资源包中未发现任何 assets/<modid>/lang/ 翻译目录: %s", pack.Path)
	}
	return mods, nil
}

func loadCfpaModsZip(path string) (map[string]bool, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	mods := map[string]bool{}
	for _, f := range zr.File {
		if m := assetLangRe.FindStringSubmatch(f.Name); m != nil {
			mods[strings.ToLower(m[1])] = true
		}
	}
	return mods, nil
}

func loadCfpaModsDir(root string) (map[string]bool, error) {
	mods := map[string]bool{}
	ds, err := os.ReadDir(filepath.Join(root, "assets"))
	if err != nil {
		if os.IsNotExist(err) {
			return mods, nil
		}
		return nil, err
	}
	for _, d := range ds {
		if !d.IsDir() {
			continue
		}
		fs, err := os.ReadDir(filepath.Join(root, "assets", d.Name(), "lang"))
		if err != nil || len(fs) == 0 {
			continue
		}
		mods[strings.ToLower(d.Name())] = true
	}
	return mods, nil
}

// deleteMods 永久删除未使用的翻译目录。
// 目录型资源包直接删除 assets/<modid>/；zip 型资源包原地重写（校验后替换原文件）。
func deleteMods(pack Pack, ids []string) (int, error) {
	if pack.IsZip {
		return rewriteZipExcluding(pack.Path, ids)
	}
	n := 0
	for _, id := range ids {
		p := filepath.Join(pack.Path, "assets", id)
		if err := os.RemoveAll(p); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// rewriteZipExcluding 把 zip 中 assets/<id>/ 前缀的条目全部剔除，重写为新 zip 并原子替换原文件。
func rewriteZipExcluding(src string, ids []string) (int, error) {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return 0, err
	}

	isSkipped := func(name string) bool {
		for _, id := range ids {
			p := "assets/" + id
			if name == p || strings.HasPrefix(name, p+"/") {
				return true
			}
		}
		return false
	}

	tmp := src + ".pruning.tmp"
	out, err := os.Create(tmp)
	if err != nil {
		zr.Close()
		return 0, err
	}
	zw := zip.NewWriter(out)
	removed := 0
	for _, f := range zr.File {
		if isSkipped(f.Name) {
			removed++
			continue
		}
		rc, err := f.Open()
		if err != nil {
			zr.Close()
			zw.Close()
			out.Close()
			os.Remove(tmp)
			return 0, err
		}
		w, err := zw.CreateHeader(&zip.FileHeader{
			Name:     f.Name,
			Method:   f.Method,
			Modified: f.Modified,
			NonUTF8:  f.NonUTF8,
		})
		if err != nil {
			rc.Close()
			zr.Close()
			zw.Close()
			out.Close()
			os.Remove(tmp)
			return 0, err
		}
		if _, err := io.Copy(w, rc); err != nil {
			rc.Close()
			zr.Close()
			zw.Close()
			out.Close()
			os.Remove(tmp)
			return 0, err
		}
		rc.Close()
	}
	// 必须先关闭源 zip 再操作原文件（Windows 下句柄占用会导致删除/重命名失败）
	want := len(zr.File) - removed
	if err := zr.Close(); err != nil {
		zw.Close()
		out.Close()
		os.Remove(tmp)
		return 0, err
	}
	if err := zw.Close(); err != nil {
		out.Close()
		os.Remove(tmp)
		return 0, err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if removed == 0 {
		os.Remove(tmp)
		return 0, nil
	}
	if err := verifyZipCount(tmp, want); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if err := os.Remove(src); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, src); err != nil {
		return 0, fmt.Errorf("替换原 zip 失败（临时文件保留在 %s）: %v", tmp, err)
	}
	return removed, nil
}

func verifyZipCount(path string, want int) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	if len(zr.File) != want {
		return fmt.Errorf("校验失败: 新 zip 条目数 %d != 预期 %d", len(zr.File), want)
	}
	return nil
}
