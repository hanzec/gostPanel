export const usesOriginalTarget = config => Boolean(config?.use_original_target || config?.use_clash_ruleset)

export const parseManualDomains = value => value.split(/\r?\n/).map(domain => domain.trim()).filter(Boolean)

export const targetRoutingFields = config => ({
  use_original_target: usesOriginalTarget(config),
  match_rules: Boolean(config?.use_clash_ruleset || config?.ruleset_ids?.length || config?.manual_domains?.length),
  ruleset_ids: config?.ruleset_ids?.length ? [...config.ruleset_ids] : config?.use_clash_ruleset ? [1] : [],
  manualDomainsText: (config?.manual_domains || []).join('\n')
})

export const isLegacyClashSelection = form => form.use_original_target && form.match_rules &&
  form.ruleset_ids.length === 1 && form.ruleset_ids[0] === 1 && !parseManualDomains(form.manualDomainsText).length

export const targetRoutingPayload = (form, previous) => {
  const legacyClash = previous.legacyClash && isLegacyClashSelection(form)
  const payload = { use_clash_ruleset: Boolean(legacyClash) }
  if (legacyClash) return payload
  if (previous.originalTarget || form.use_original_target) payload.use_original_target = form.use_original_target
  if (previous.ruleSelection || form.use_original_target) {
    payload.ruleset_ids = form.use_original_target && form.match_rules ? [...form.ruleset_ids] : []
    payload.manual_domains = form.use_original_target && form.match_rules ? parseManualDomains(form.manualDomainsText) : []
  }
  return payload
}
