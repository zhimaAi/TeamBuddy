// 与 Go 的 executor.DefaultCLIModel 一致；只用于非 Vibe 的执行配置。
export const DEFAULT_CLI_MODEL = '@teambuddy/default'

export function cliModelFamily(cliType: string, model: string, defaultLabel = model): string {
  if (model === DEFAULT_CLI_MODEL) return defaultLabel
  // 只合并 Antigravity 原生档位的展示名；可选强度仍由选中模型的原生 ACP 元数据决定。
  return cliType === 'antigravity' ? model.replace(/-(low|medium|high|xhigh|max)$/, '') : model
}

export function cliModelChoices(cliType: string, models: readonly string[], selected = '', defaultLabel?: string) {
  const choices = new Map<string, { value: string; label: string }>()
  if (cliType && defaultLabel) choices.set(DEFAULT_CLI_MODEL, { value: DEFAULT_CLI_MODEL, label: defaultLabel })
  for (const model of models) {
    if (model === DEFAULT_CLI_MODEL) continue
    const label = cliModelFamily(cliType, model)
    const existing = choices.get(label)
    if (!existing) choices.set(label, { value: model, label })
    // 回显时沿用已保存的原生变体，打开选择器不改变“跟随 CLI”的实际档位。
    else if (model === selected) existing.value = model
  }
  if (selected && !choices.has(selected) && !models.includes(selected)) choices.set(selected, { value: selected, label: selected })
  return [...choices.values()]
}
