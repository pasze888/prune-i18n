package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// ReportData 汇总一次运行的全部对照结果，用于生成审计报告。
type ReportData struct {
	Time         time.Time
	Root         string
	Pack         Pack
	Jars         []JarInfo
	CfpaCount    int
	DryRun       bool
	Cancelled    bool
	KeptFixed    []string // 固定保留（原版/加载器）
	KeptAuto     []string // jar 元数据自动匹配
	KeptFilename []string // 文件名兜底匹配
	KeptLLM      []string // LLM 判定保留
	Deleted      []string
	LLMNotes     []string
}

func writeReport(path string, d ReportData) error {
	var b strings.Builder
	b.WriteString("# i18n 修剪报告\n\n")
	fmt.Fprintf(&b, "- 生成时间: %s\n", d.Time.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- 整合包根目录: %s\n", d.Root)
	fmt.Fprintf(&b, "- CFPA 资源包: %s（%s）\n", d.Pack.Name, packType(d.Pack))
	if d.DryRun {
		b.WriteString("- 模式: **dry-run**（仅计划，未修改任何文件）\n")
	}
	if d.Cancelled {
		b.WriteString("- 结果: 用户取消删除，未修改任何文件\n")
	}

	var parsed, uncertain int
	for _, j := range d.Jars {
		if j.Uncertain {
			uncertain++
		} else {
			parsed++
		}
	}
	fmt.Fprintf(&b, "\n## 统计\n\n")
	fmt.Fprintf(&b, "- jar 数量: %d（解析出 modid: %d，无法解析: %d）\n", len(d.Jars), parsed, uncertain)
	fmt.Fprintf(&b, "- CFPA 翻译目录总数: %d\n", d.CfpaCount)
	fmt.Fprintf(&b, "- 固定保留: %d\n- 自动匹配保留: %d\n- 文件名匹配保留: %d\n- LLM 判定保留: %d\n- 删除: %d\n",
		len(d.KeptFixed), len(d.KeptAuto), len(d.KeptFilename), len(d.KeptLLM), len(d.Deleted))

	b.WriteString("\n## 删除的翻译目录\n\n")
	if len(d.Deleted) == 0 {
		b.WriteString("（无）\n")
	}
	for _, id := range d.Deleted {
		fmt.Fprintf(&b, "- `assets/%s/`\n", id)
	}

	b.WriteString("\n## 保留的翻译目录\n\n")
	b.WriteString("### 固定保留（原版/加载器）\n")
	listOrNone(&b, d.KeptFixed)
	b.WriteString("### 自动匹配（jar 元数据 modid）\n")
	listOrNone(&b, d.KeptAuto)
	b.WriteString("### 文件名匹配\n")
	listOrNone(&b, d.KeptFilename)
	b.WriteString("### LLM 判定保留\n")
	listOrNone(&b, d.KeptLLM)

	b.WriteString("\n## LLM 判定明细\n\n")
	if len(d.LLMNotes) == 0 {
		b.WriteString("（无）\n")
	}
	for _, n := range d.LLMNotes {
		b.WriteString("- " + n + "\n")
	}

	b.WriteString("\n## 无法解析 modid 的 jar\n\n")
	if uncertain == 0 {
		b.WriteString("（无）\n")
	}
	for _, j := range d.Jars {
		if !j.Uncertain {
			continue
		}
		fmt.Fprintf(&b, "### %s（%d 字节）\n", j.Name, j.Size)
		if j.ParseErr != "" {
			b.WriteString(j.ParseErr + "\n")
		}
		if len(j.Entries) > 0 {
			b.WriteString("顶层内容: `" + strings.Join(j.Entries, ", ") + "`\n")
		}
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func listOrNone(b *strings.Builder, items []string) {
	if len(items) == 0 {
		b.WriteString("（无）\n")
		return
	}
	for _, id := range items {
		fmt.Fprintf(b, "- `%s`\n", id)
	}
}
