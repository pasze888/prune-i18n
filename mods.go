package main

import (
	"archive/zip"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// JarInfo 描述整合包 mods 目录下的一个 jar 文件及其解析结果。
type JarInfo struct {
	Name      string   // 文件名，如 jei-1.20.1.jar
	Path      string   // 完整路径
	Size      int64    // 文件大小（字节）
	ModIDs    []string // 解析出的 modid（可能多个）
	Provided  []string // fabric provides 声明的次要 id
	Uncertain bool     // 无法解析出任何 modid
	ParseErr  string   // 打开/解析失败的原因（仅 Uncertain 时可能有值）
	Entries   []string // jar 内顶层条目（供 LLM 识别），最多 40 个
}

// scanMods 递归扫描目录下的所有 .jar 文件（排除 *.disabled），按文件名排序。
func scanMods(dir string) []JarInfo {
	var jars []JarInfo
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if !strings.HasSuffix(name, ".jar") || strings.HasSuffix(name, ".disabled") {
			return nil
		}
		j, _ := parseJar(path)
		jars = append(jars, j)
		return nil
	})
	sort.Slice(jars, func(i, k int) bool { return jars[i].Name < jars[k].Name })
	return jars
}

// parseJar 在内存中读取 jar（即"临时解压查看"的自动化），提取 modid。
func parseJar(path string) (JarInfo, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return JarInfo{Name: filepath.Base(path), Path: path, Uncertain: true,
			ParseErr: "无法打开 jar: " + err.Error()}, err
	}
	defer zr.Close()

	info := JarInfo{Name: filepath.Base(path), Path: path}
	if fi, err := os.Stat(path); err == nil {
		info.Size = fi.Size()
	}

	ids := map[string]bool{}
	provided := map[string]bool{}
	entries := map[string]bool{}

	for _, f := range zr.File {
		// 记录顶层条目，供 LLM 识别未知 jar
		if i := strings.IndexByte(f.Name, '/'); i >= 0 {
			entries[f.Name[:i]] = true
		} else {
			entries[f.Name] = true
		}

		var data []byte
		var perr error
		switch f.Name {
		case "fabric.mod.json":
			data, perr = readZipFile(f)
			if perr == nil {
				perr = parseFabric(data, ids, provided)
			}
		case "quilt.mod.json":
			data, perr = readZipFile(f)
			if perr == nil {
				perr = parseQuilt(data, ids)
			}
		case "META-INF/mods.toml", "META-INF/neoforge.mods.toml":
			data, perr = readZipFile(f)
			if perr == nil {
				for _, id := range extractModIDs(string(data)) {
					ids[id] = true
				}
			}
		case "mcmod.info":
			data, perr = readZipFile(f)
			if perr == nil {
				perr = parseMcmodInfo(data, ids)
			}
		}
		if perr != nil && info.ParseErr == "" {
			info.ParseErr = "解析 " + f.Name + " 失败: " + perr.Error()
		}
	}

	for e := range entries {
		info.Entries = append(info.Entries, e)
	}
	sort.Strings(info.Entries)
	if len(info.Entries) > 40 {
		info.Entries = info.Entries[:40]
	}
	for id := range ids {
		info.ModIDs = append(info.ModIDs, id)
	}
	for id := range provided {
		info.Provided = append(info.Provided, id)
	}
	sort.Strings(info.ModIDs)
	sort.Strings(info.Provided)
	info.Uncertain = len(info.ModIDs) == 0
	return info, nil
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, 16<<20))
}

// parseFabric 解析 fabric.mod.json（含 v2 的 provides 字段）。
func parseFabric(data []byte, ids, provided map[string]bool) error {
	var fm struct {
		ID       string `json:"id"`
		Provides []struct {
			ID string `json:"id"`
		} `json:"provides"`
	}
	if err := json.Unmarshal(data, &fm); err != nil {
		return err
	}
	if fm.ID != "" {
		ids[strings.ToLower(fm.ID)] = true
	}
	for _, p := range fm.Provides {
		if p.ID != "" {
			provided[strings.ToLower(p.ID)] = true
		}
	}
	return nil
}

// parseQuilt 解析 quilt.mod.json。
func parseQuilt(data []byte, ids map[string]bool) error {
	var qm struct {
		QuiltLoader struct {
			ID string `json:"id"`
		} `json:"quilt_loader"`
	}
	if err := json.Unmarshal(data, &qm); err != nil {
		return err
	}
	if qm.QuiltLoader.ID != "" {
		ids[strings.ToLower(qm.QuiltLoader.ID)] = true
	}
	return nil
}

// parseMcmodInfo 解析老版本（1.12 及更早）的 mcmod.info。
func parseMcmodInfo(data []byte, ids map[string]bool) error {
	var arr []struct {
		Modid string `json:"modid"`
	}
	if err := json.Unmarshal(data, &arr); err != nil {
		var obj struct {
			Modid string `json:"modid"`
		}
		if err2 := json.Unmarshal(data, &obj); err2 != nil {
			return err
		}
		if obj.Modid != "" {
			ids[strings.ToLower(obj.Modid)] = true
		}
		return nil
	}
	for _, m := range arr {
		if m.Modid != "" {
			ids[strings.ToLower(m.Modid)] = true
		}
	}
	return nil
}

var modIDRe = regexp.MustCompile(`(?i)modId\s*=\s*['"]([^'"]+)['"]`)

// extractModIDs 从 mods.toml / neoforge.mods.toml 文本中提取全部 modId。
// Go 标准库没有 TOML 解析器；剥离注释后正则提取对真实文件及损坏文件均可靠。
func extractModIDs(toml string) []string {
	lines := strings.Split(toml, "\n")
	var sb strings.Builder
	for _, ln := range lines {
		if i := commentIndex(ln); i >= 0 {
			ln = ln[:i]
		}
		sb.WriteString(ln)
		sb.WriteByte('\n')
	}
	var ids []string
	seen := map[string]bool{}
	for _, m := range modIDRe.FindAllStringSubmatch(sb.String(), -1) {
		id := strings.ToLower(strings.TrimSpace(m[1]))
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// commentIndex 返回 TOML 行内注释 '#' 的位置（行首或前有空白才算），否则 -1。
func commentIndex(line string) int {
	for i := 0; i < len(line); i++ {
		if line[i] == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return i
		}
	}
	return -1
}

// platformTokens: jar 文件名中常见的加载器平台后缀，避免把 "jei-1.20.1-forge.jar" 误匹配为 modid "forge"。
var platformTokens = map[string]bool{
	"forge": true, "fml": true, "neoforge": true, "fabric": true, "quilt": true,
}

// matchesJarName 判断 CFPA modid 能否由某个 jar 文件名兜底匹配：
// 完整文件名（去 .jar）精确相等，或按 -_. 空白切分出的片段精确相等（仅限长度 >= 3 的 modid，且排除平台后缀，避免误判）。
func matchesJarName(modid string, jars []JarInfo) bool {
	for _, j := range jars {
		base := strings.ToLower(strings.TrimSuffix(j.Name, ".jar"))
		if base == modid {
			return true
		}
		if len(modid) < 3 {
			continue
		}
		for _, s := range strings.FieldsFunc(base, func(r rune) bool {
			return r == '-' || r == '_' || r == '.' || r == ' '
		}) {
			if s == modid && !platformTokens[s] {
				return true
			}
		}
	}
	return false
}
