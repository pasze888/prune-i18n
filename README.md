# prune-i18n — CFPA 汉化资源包修剪工具

对照整合包 `mods/` 中的模组，删除 CFPA 汉化资源包（[Minecraft-Mod-Language-Modpack](https://github.com/CFPAOrg/Minecraft-Mod-Language-Modpack)）中整合包用不到的翻译目录，只保留需要的汉化。

- **纯 Go 标准库实现**，单文件 exe，无需 Python / Node / JRE 等任何运行时
- 自动从 jar 中解析 modid（Fabric / Quilt / Forge / NeoForge / 老版本 `mcmod.info`），解析不了的 jar 自动提取内部文件清单
- 无法自动对照的部分交给 **LLM 判断**：base_url / API Key / 模型名均为交互式输入，**不写入任何文件**（API Key 输入时不在屏幕回显）
- 每次运行生成中文审计报告（保留/删除/LLM 判定明细）

## 使用

1. 下载 CFPA 汉化资源包，解压或直接放入整合包的 `resourcepacks/` 目录（zip 形式也可以，程序会原地重写；建议解压成文件夹）
2. 把 `prune-i18n.exe` 复制到**整合包根目录**（即含 `mods/` 和 `resourcepacks/` 的目录）
3. 双击运行（或在命令行运行 `prune-i18n.exe`）
4. 程序自动扫描 `mods/` 与 `resourcepacks/`：
   - 只有一个资源包 → 自动选用；多个 → 列出供你选择
   - **默认不调用 LLM**：未被匹配的翻译目录直接进入删除列表；需要 LLM 对照时加 `-llm`，程序会交互输入 base_url、API Key、模型名并自动调用
5. 确认后**永久删除**未使用的翻译目录，并生成 `i18n_prune_report_<时间戳>.md` 报告

```
prune-i18n.exe
```

## 命令行选项

| 选项 | 说明 |
| --- | --- |
| `-dir <路径>` | 整合包根目录（默认当前目录） |
| `-pack <路径>` | 直接指定 CFPA 资源包（目录或 zip），跳过 resourcepacks 扫描与选择 |
| `-dry-run` | 只输出计划，不执行删除 |
| `-yes` | 跳过删除前确认 |
| `-llm` | 启用 LLM 对照环节（**默认关闭**；关闭时未匹配的一律删除；开启后交互输入 LLM 配置） |
| `-verbose` | 显示各阶段详细过程：jar 解析明细、匹配明细、LLM 环节（若启用）的提示词/请求/回复/决策、删除执行 |
| `-apply-decisions <json>` | 应用 LLM 决策 JSON 文件（见下），跳过 LLM 调用 |
| `-keep <modid,...>` | 额外保留的 modid（默认已固定保留 `minecraft/forge/fml/neoforge`） |
| `-report <路径>` | 报告输出路径（默认 `<根目录>/i18n_prune_report_<时间戳>.md`） |

示例：

```
prune-i18n.exe -dry-run                    # 先看计划
prune-i18n.exe -llm                        # 启用 LLM 对照（交互输入配置）
prune-i18n.exe -keep kubejs,ftbquests      # 额外保留某些目录
prune-i18n.exe -pack D:\packs\CFPA.zip     # 直接指定资源包
```

## 环境变量（可选，免交互）

不想手动输入时可用环境变量提供 LLM 配置（仅 `-llm` 启用时生效，仍不会写入任何文件）：

- `LLM_BASE_URL`（默认 `https://api.openai.com/v1`）
- `LLM_API_KEY`
- `LLM_MODEL`（默认 `gpt-4o-mini`）

## 工作原理

1. **提取 CFPA 模组集**：扫描资源包 `assets/<modid>/lang/` 下的翻译目录名
2. **提取整合包模组集**：逐个读取 `mods/*.jar`（等价于自动"临时解压查看"）：
   - `fabric.mod.json` → `id`（含 v2 `provides`）；`quilt.mod.json` → `quilt_loader.id`
   - `META-INF/mods.toml` / `neoforge.mods.toml` → 剥离注释后提取全部 `modId`（Go 标准库无 TOML 解析器，正则方案对真实及损坏文件均可靠）
   - `mcmod.info` → 老版本 modid；全部失败 → 记入"无法解析"清单，附 jar 内顶层条目
3. **自动对照**：翻译目录命中 jar 元数据 → 保留；未命中再用 jar 文件名（去版本号）兜底；剩余为**删除候选**
4. **LLM 对照**：把删除候选 + 无法解析的 jar（含内容摘要）生成中文提示词，调用 OpenAI 兼容 `/chat/completions`，要求返回严格 JSON（`jar_mappings` / `extra_keeps`），程序校验后应用
5. **执行**：永久删除（`--dry-run` 可预览）；zip 型资源包重写后校验条目数再原子替换原文件

### LLM 失败兜底

LLM 调用失败时，待判断内容会写入 `llm_prompt_<时间戳>.txt`。你可以把内容粘贴给任意 LLM，把它的回答保存成 JSON 文件（格式见提示词末尾），再运行：

```
prune-i18n.exe -apply-decisions decisions.json
```

## 说明与风险

- 删除**不可恢复**，首次使用建议先 `-dry-run` 查看计划
- 固定保留 `minecraft`（原版中文，比游戏自带更全）及 `forge/fml/neoforge` 加载器翻译，`-keep` 可追加
- 无法解析 modid 的 jar（如非模组的库文件）不会导致误删——它们只进入 LLM 提示词，不会主动保留任何目录
- `resourcepacks/` 下有多个资源包时务必选对 CFPA 包，否则会误删其他包内容

## 自行编译

需要 Go 1.21+：

```
go build -o prune-i18n.exe .
go test ./...
```
