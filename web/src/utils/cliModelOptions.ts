export function cliModelFamily(cliType: string, model: string): string {
  // 只合并 Antigravity 原生档位的展示名；可选强度仍由 /effort 元数据决定。
  return cliType === 'antigravity' ? model.replace(/-(low|medium|high|xhigh|max)$/, '') : model
}

export function cliModelChoices(cliType: string, models: readonly string[], selected = '') {
  const choices = new Map<string, { value: string; label: string }>()
  for (const model of models) {
    const label = cliModelFamily(cliType, model)
    const existing = choices.get(label)
    if (!existing) choices.set(label, { value: model, label })
    // 回显时沿用已保存的原生变体，打开选择器不改变“跟随 CLI”的实际档位。
    else if (model === selected) existing.value = model
  }
  if (selected && !models.includes(selected)) choices.set(selected, { value: selected, label: selected })
  return [...choices.values()]
}
