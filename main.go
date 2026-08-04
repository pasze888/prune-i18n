package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Options 命令行选项。
type Options struct {
	Root           string // 整合包根目录
	PackPath       string // 显式指定 CFPA 资源包路径（跳过 resourcepacks 扫描）
	DryRun         bool
	Yes            bool // 跳过删除前确认
	UseLLM         bool // 启用 LLM 对照环节（默认关闭）
	Verbose        bool // 显示各阶段详细过程
	ApplyDecisions string
	Keep           map[string]bool
	Report         string
}

func main() {
	os.Exit(runCLI(os.Args[1:]))
}

func runCLI(args []string) int {
	setupConsoleUTF8()
	fs := flag.NewFlagSet("prune-i18n", flag.ContinueOnError)
	root := fs.String("dir", ".", "整合包根目录（默认当前目录）")
	packPath := fs.String("pack", "", "直接指定 CFPA 资源包路径（目录或 zip），跳过 resourcepacks 扫描")
	dryRun := fs.Bool("dry-run", false, "仅输出计划，不执行删除")
	yes := fs.Bool("yes", false, "跳过删除前确认")
	llm := fs.Bool("llm", false, "启用 LLM 对照环节（默认关闭；开启后交互输入 LLM 配置）")
	verbose := fs.Bool("verbose", false, "显示各阶段详细过程（jar 解析、匹配明细、LLM 环节、删除执行）")
	apply := fs.String("apply-decisions", "", "应用 LLM 决策 JSON 文件，跳过 LLM 调用")
	keep := fs.String("keep", "", "额外保留的 modid，逗号分隔")
	report := fs.String("report", "", "报告输出路径（默认 <根目录>/i18n_prune_report_<时间戳>.md）")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "prune-i18n — 修剪 CFPA 汉化资源包（对照整合包 mods，删除未使用的翻译目录）\n\n")
		fmt.Fprintf(os.Stderr, "用法: prune-i18n.exe [选项]\n\n")
		fmt.Fprintf(os.Stderr, "把程序放到整合包根目录（含 mods/ 与 resourcepacks/）后直接运行；\n")
		fmt.Fprintf(os.Stderr, "默认不调用 LLM，未匹配的一律删除；加 -llm 启用 LLM 对照时，\n")
		fmt.Fprintf(os.Stderr, "LLM 配置（base_url / API Key / 模型）为交互式输入，不写入任何文件。\n\n选项:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "不支持的参数:", fs.Args())
		return 2
	}

	keepSet := defaultKeepSet()
	for _, k := range strings.Split(*keep, ",") {
		k = strings.ToLower(strings.TrimSpace(k))
		if k != "" {
			keepSet[k] = true
		}
	}
	opts := &Options{
		Root:           *root,
		PackPath:       *packPath,
		DryRun:         *dryRun,
		Yes:            *yes,
		UseLLM:         *llm,
		Verbose:        *verbose,
		ApplyDecisions: *apply,
		Keep:           keepSet,
		Report:         *report,
	}
	if err := run(opts, askLLMConfig); err != nil {
		fmt.Fprintf(os.Stderr, "\n错误: %v\n", err)
		return 1
	}
	return 0
}

func defaultKeepSet() map[string]bool {
	return map[string]bool{
		"minecraft": true, // 原版翻译（CFPA 比游戏自带更全）
		"forge":     true, // Forge 加载器内置翻译
		"fml":       true, // 老版本 Forge
		"neoforge":  true, // NeoForge 加载器内置翻译
	}
}

func run(opts *Options, ask func() (LLMConfig, error)) error {
	root := opts.Root
	fmt.Println("prune-i18n — CFPA 汉化资源包修剪工具")
	fmt.Println("整合包根目录:", root)
	if opts.Verbose {
		fmt.Printf("（verbose）选项: dry-run=%v yes=%v llm=%v apply-decisions=%q\n",
			opts.DryRun, opts.Yes, opts.UseLLM, opts.ApplyDecisions)
		fmt.Printf("（verbose）固定保留集: %v\n", sortedKeys(opts.Keep))
	}

	modsDir := filepath.Join(root, "mods")
	if fi, err := os.Stat(modsDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("未找到 mods 目录: %s（请把程序放在整合包根目录，或用 -dir 指定）", modsDir)
	}
	fmt.Println("正在扫描 mods 目录...")
	jars := scanMods(modsDir)
	if len(jars) == 0 {
		return fmt.Errorf("mods 目录下未找到任何 .jar 文件: %s", modsDir)
	}
	if opts.Verbose {
		fmt.Printf("（verbose）扫描到 %d 个 jar：\n", len(jars))
		for _, j := range jars {
			if j.Uncertain {
				fmt.Printf("  %s（%d 字节）→ 无法解析 modid%s\n", j.Name, j.Size, parseErrSuffix(j))
				if len(j.Entries) > 0 {
					fmt.Printf("    顶层内容: %v\n", j.Entries)
				}
			} else {
				fmt.Printf("  %s（%d 字节）→ modid: %v", j.Name, j.Size, j.ModIDs)
				if len(j.Provided) > 0 {
					fmt.Printf("（provides: %v）", j.Provided)
				}
				fmt.Println()
			}
		}
	}

	pack, err := selectPack(opts, root)
	if err != nil {
		return err
	}
	fmt.Printf("CFPA 资源包: %s（%s，%d 个模组翻译目录）\n", pack.Name, packType(pack), pack.ModCount)

	fmt.Println("正在读取资源包模组列表...")
	cfpaMods, err := loadCfpaMods(pack)
	if err != nil {
		return err
	}
	if opts.Verbose {
		fmt.Printf("（verbose）资源包翻译目录（%d 个）:\n", len(cfpaMods))
		printListCapped("  ", sortedKeys(cfpaMods), 200)
	}

	// 汇总 jar 中解析出的全部 modid（含 provides 次要 id）
	known := map[string]bool{}
	for _, j := range jars {
		for _, id := range j.ModIDs {
			known[id] = true
		}
		for _, id := range j.Provided {
			known[id] = true
		}
	}

	rep := &ReportData{
		Time:      time.Now(),
		Root:      root,
		Pack:      pack,
		Jars:      jars,
		CfpaCount: len(cfpaMods),
		DryRun:    opts.DryRun,
	}

	// 第一轮对照：固定保留 / 元数据匹配 / 其余为删除候选
	candidates := map[string]bool{}
	for id := range cfpaMods {
		switch {
		case opts.Keep[id]:
			rep.KeptFixed = append(rep.KeptFixed, id)
		case known[id]:
			rep.KeptAuto = append(rep.KeptAuto, id)
		default:
			candidates[id] = true
		}
	}
	// 第二轮：jar 文件名兜底匹配
	for id := range candidates {
		if matchesJarName(id, jars) {
			delete(candidates, id)
			rep.KeptFilename = append(rep.KeptFilename, id)
		}
	}
	sort.Strings(rep.KeptFixed)
	sort.Strings(rep.KeptAuto)
	sort.Strings(rep.KeptFilename)

	if opts.Verbose {
		fmt.Printf("（verbose）jar 解析出的全部 modid（%d 个）:\n", len(known))
		printListCapped("  ", sortedKeys(known), 200)
		fmt.Println("（verbose）固定保留:")
		printListCapped("  ", rep.KeptFixed, 100)
		fmt.Println("（verbose）自动匹配保留:")
		printListCapped("  ", rep.KeptAuto, 100)
		fmt.Println("（verbose）文件名匹配保留:")
		printListCapped("  ", rep.KeptFilename, 100)
		fmt.Println("（verbose）删除候选:")
		printListCapped("  ", sortedKeys(candidates), 200)
	}

	var uncertain []JarInfo
	for _, j := range jars {
		if j.Uncertain {
			uncertain = append(uncertain, j)
		}
	}

	// 第三轮：LLM 对照（仅当存在删除候选时才有意义）
	llmRan := false
	if len(candidates) > 0 {
		switch {
		case opts.ApplyDecisions != "":
			fmt.Println("正在应用 LLM 决策文件:", opts.ApplyDecisions)
			dec, err := loadDecisionFile(opts.ApplyDecisions)
			if err != nil {
				return fmt.Errorf("读取决策文件失败: %v", err)
			}
			if opts.Verbose {
				printVerboseDecision(dec)
			}
			rep.KeptLLM, rep.LLMNotes = applyDecision(dec, candidates, cfpaMods, jars)
			if opts.Verbose {
				printVerboseNotes(rep.LLMNotes)
			}
		case !opts.UseLLM:
			fmt.Println("（默认关闭 LLM 环节）未匹配的一律删除；可用 -llm 启用 LLM 对照")
			if opts.Verbose {
				fmt.Printf("（verbose）删除候选 %d 个，未经 LLM 确认直接进入删除列表\n", len(candidates))
			}
		default:
			cfg, err := resolveLLMConfig(ask)
			if err != nil {
				return err
			}
			fmt.Printf("正在调用 LLM（%s）...\n", cfg.Model)
			prompt := buildPrompt(sortedKeys(candidates), uncertain)
			if opts.Verbose {
				fmt.Printf("（verbose）LLM 请求: POST %s/chat/completions（模型 %s，提示词 %d 字符）\n",
					strings.TrimRight(cfg.BaseURL, "/"), cfg.Model, len(prompt))
				fmt.Println("（verbose）===== 发送给 LLM 的提示词 =====")
				fmt.Println(prompt)
				fmt.Println("（verbose）===== 提示词结束 =====")
			}
			start := time.Now()
			reply, err := callLLM(cfg, prompt)
			if err != nil {
				p := filepath.Join(root, fmt.Sprintf("llm_prompt_%s.txt", time.Now().Format("20060102_150405")))
				if werr := os.WriteFile(p, []byte(prompt), 0o644); werr != nil {
					return fmt.Errorf("LLM 调用失败: %v（提示文件写入也失败: %v）", err, werr)
				}
				return fmt.Errorf("LLM 调用失败: %v\n已把待 LLM 判断的内容写入 %s；可用任意 LLM 回答后保存为 JSON，再以 -apply-decisions 指定重跑", err, p)
			}
			if opts.Verbose {
				fmt.Printf("（verbose）LLM 响应耗时: %s\n", time.Since(start).Round(time.Millisecond))
				fmt.Println("（verbose）===== LLM 原始回复 =====")
				fmt.Println(reply)
				fmt.Println("（verbose）===== 回复结束 =====")
			}
			dec, err := parseDecision(reply)
			if err != nil {
				return fmt.Errorf("LLM 返回内容解析失败: %v（原始回复: %.200s）", err, reply)
			}
			if opts.Verbose {
				printVerboseDecision(dec)
			}
			rep.KeptLLM, rep.LLMNotes = applyDecision(dec, candidates, cfpaMods, jars)
			if opts.Verbose {
				printVerboseNotes(rep.LLMNotes)
			}
			llmRan = true
		}
	} else if len(uncertain) > 0 {
		fmt.Println("无删除候选，跳过 LLM 环节（存在无法解析的 jar，已记入报告）")
		if opts.Verbose {
			fmt.Printf("（verbose）无法解析的 jar %d 个，但它们不影响任何删除候选，无需 LLM 判断\n", len(uncertain))
		}
	} else if opts.Verbose {
		fmt.Println("（verbose）所有 CFPA 翻译目录均已自动匹配，无需 LLM 环节")
	}

	deletion := sortedKeys(candidates)
	rep.Deleted = deletion

	// 输出对照摘要
	var parsedN int
	for _, j := range jars {
		if !j.Uncertain {
			parsedN++
		}
	}
	fmt.Printf("\n== 对照结果 ==\n")
	fmt.Printf("jar 数量: %d（解析出 modid %d 个，无法解析 %d 个）\n", len(jars), parsedN, len(jars)-parsedN)
	fmt.Printf("CFPA 翻译目录: %d\n", len(cfpaMods))
	fmt.Printf("固定保留: %d 个, 自动匹配保留: %d 个, 文件名匹配保留: %d 个",
		len(rep.KeptFixed), len(rep.KeptAuto), len(rep.KeptFilename))
	if llmRan || opts.ApplyDecisions != "" {
		fmt.Printf(", LLM 判定保留: %d 个", len(rep.KeptLLM))
	}
	fmt.Printf("\n待删除: %d 个\n", len(deletion))
	for _, id := range deletion {
		fmt.Printf("  - assets/%s\n", id)
	}

	// 执行（dry-run / 确认 / 删除）
	switch {
	case opts.DryRun:
		fmt.Printf("\n（dry-run）共将删除 %d 个目录，本次未修改任何文件。\n", len(deletion))
	case len(deletion) == 0:
		fmt.Println("\n无需删除。")
	default:
		if !opts.Yes {
			ok, err := askConfirm("确认永久删除以上目录？输入 y 继续（其他任意键取消）: ")
			if err != nil {
				return err
			}
			if !ok {
				rep.Cancelled = true
				fmt.Println("已取消，未做任何修改。")
			}
		}
		if !rep.Cancelled {
			if opts.Verbose {
				fmt.Println("（verbose）开始删除:")
				for _, id := range deletion {
					fmt.Printf("  - assets/%s/\n", id)
				}
			}
			fmt.Println("正在删除...")
			n, err := deleteMods(pack, deletion)
			if err != nil {
				return fmt.Errorf("删除失败（已删除 %d 个）: %v", n, err)
			}
			fmt.Printf("已删除 %d 个目录。\n", n)
			if opts.Verbose && pack.IsZip {
				fmt.Println("（verbose）zip 资源包已重写并校验、原子替换原文件")
			}
		}
	}

	path := opts.Report
	if path == "" {
		path = filepath.Join(root, fmt.Sprintf("i18n_prune_report_%s.md", time.Now().Format("20060102_150405")))
	}
	if err := writeReport(path, *rep); err != nil {
		return fmt.Errorf("写报告失败: %v", err)
	}
	fmt.Println("报告已写入:", path)
	if opts.Verbose {
		if fi, err := os.Stat(path); err == nil {
			fmt.Printf("（verbose）报告大小: %d 字节\n", fi.Size())
		}
	}
	return nil
}

// selectPack 选择 CFPA 资源包：显式指定 > resourcepacks 唯一候选 > 交互选择。
func selectPack(opts *Options, root string) (Pack, error) {
	if opts.PackPath != "" {
		p, err := openPack(opts.PackPath)
		if err != nil {
			return Pack{}, err
		}
		if opts.Verbose {
			fmt.Printf("（verbose）指定的资源包: %s（%s）\n", p.Path, packType(p))
		}
		return p, nil
	}
	respDir := filepath.Join(root, "resourcepacks")
	packs, err := detectPacks(respDir)
	if err != nil {
		if os.IsNotExist(err) {
			return Pack{}, fmt.Errorf("未找到 resourcepacks 目录: %s", respDir)
		}
		return Pack{}, fmt.Errorf("扫描 resourcepacks 失败: %v", err)
	}
	if len(packs) == 0 {
		return Pack{}, fmt.Errorf("resourcepacks 目录下未找到任何资源包（需包含 pack.mcmeta）")
	}
	if opts.Verbose {
		fmt.Printf("（verbose）resourcepacks 下检测到 %d 个资源包:\n", len(packs))
		for _, p := range packs {
			fmt.Printf("  - %s（%s，%d 个翻译目录）\n", p.Path, packType(p), p.ModCount)
		}
	}
	if len(packs) == 1 {
		fmt.Println("检测到资源包:", packs[0].Name)
		if opts.Verbose {
			fmt.Printf("（verbose）路径: %s\n", packs[0].Path)
		}
		return packs[0], nil
	}
	fmt.Println("检测到多个资源包，请选择 CFPA 翻译包：")
	for i, p := range packs {
		fmt.Printf("  %d. %s（%s，%d 个翻译目录）\n", i+1, p.Name, packType(p), p.ModCount)
	}
	n, err := askInt("输入序号: ", 1, len(packs))
	if err != nil {
		return Pack{}, err
	}
	return packs[n-1], nil
}

// ----- 交互输入 -----

// stdin 是全局共享的标准输入读取器：多个输入函数若各自创建 bufio.Reader，
// 前一个会把管道输入预读进自己的缓冲区，导致后续输入读到空。
var stdin = bufio.NewReader(os.Stdin)

func readLine() (string, error) {
	line, err := stdin.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func askString(prompt, def string) (string, error) {
	fmt.Print(prompt)
	line, err := readLine()
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(line)
	if v == "" {
		return def, nil
	}
	return v, nil
}

func askInt(prompt string, min, max int) (int, error) {
	for {
		s, err := askString(prompt, "")
		if err != nil {
			return 0, err
		}
		n, err := strconv.Atoi(s)
		if err == nil && n >= min && n <= max {
			return n, nil
		}
		fmt.Printf("请输入 %d-%d 之间的数字。\n", min, max)
	}
}

func askConfirm(prompt string) (bool, error) {
	s, err := askString(prompt, "")
	if err != nil {
		return false, err
	}
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes", nil
}

// askLLMConfig 交互式收集 LLM 配置（API Key 不回显，不写入任何文件）。
func askLLMConfig() (LLMConfig, error) {
	base, err := askString("LLM API 地址（base_url，默认 https://api.openai.com/v1）: ", "https://api.openai.com/v1")
	if err != nil {
		return LLMConfig{}, err
	}
	key, err := readSecret("LLM API Key（输入不回显）: ")
	if err != nil {
		return LLMConfig{}, err
	}
	if strings.TrimSpace(key) == "" {
		return LLMConfig{}, fmt.Errorf("未输入 API Key")
	}
	model, err := askString("模型名（默认 gpt-4o-mini）: ", "gpt-4o-mini")
	if err != nil {
		return LLMConfig{}, err
	}
	return LLMConfig{
		BaseURL: strings.TrimSpace(base),
		APIKey:  strings.TrimSpace(key),
		Model:   strings.TrimSpace(model),
	}, nil
}

func sortedKeys(m map[string]bool) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func sortedKeysString(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// printListCapped 打印列表，超过 cap 条时省略中间部分并提示总数。
func printListCapped(prefix string, items []string, cap int) {
	if len(items) == 0 {
		fmt.Println(prefix + "（空）")
		return
	}
	shown := items
	if len(items) > cap {
		shown = items[:cap]
	}
	for _, it := range shown {
		fmt.Println(prefix + it)
	}
	if len(items) > cap {
		fmt.Printf("%s…共 %d 条，省略 %d 条\n", prefix, len(items), len(items)-cap)
	}
}

// parseErrSuffix 返回 jar 解析失败原因的括号后缀（用于 verbose 输出）。
func parseErrSuffix(j JarInfo) string {
	if j.ParseErr == "" {
		return ""
	}
	return "（" + j.ParseErr + "）"
}

// printVerboseDecision 在 verbose 模式下打印解析出的 LLM 决策。
func printVerboseDecision(dec Decision) {
	fmt.Println("（verbose）===== 解析出的 LLM 决策 =====")
	fmt.Println("jar_mappings:")
	if len(dec.JarMappings) == 0 {
		fmt.Println("  （空）")
	}
	for _, jar := range sortedKeysString(dec.JarMappings) {
		if id := dec.JarMappings[jar]; id == "" {
			fmt.Printf("  %s → null（无对应 modid）\n", jar)
		} else {
			fmt.Printf("  %s → %s\n", jar, id)
		}
	}
	fmt.Printf("extra_keeps: %v\n", dec.ExtraKeeps)
	fmt.Println("（verbose）===== 决策结束 =====")
}

// printVerboseNotes 在 verbose 模式下打印决策应用明细（保留/警告）。
func printVerboseNotes(notes []string) {
	fmt.Println("（verbose）===== 决策应用明细 =====")
	if len(notes) == 0 {
		fmt.Println("  （无——LLM 决策未影响任何候选）")
	}
	for _, n := range notes {
		fmt.Println("  " + n)
	}
	fmt.Println("（verbose）===== 明细结束 =====")
}
