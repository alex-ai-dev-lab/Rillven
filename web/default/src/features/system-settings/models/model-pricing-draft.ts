import { combineBillingExpr } from '@/features/pricing/lib/billing-expr'
import type { ModelRatioData } from './model-pricing-sheet'

export type PricingMaps = {
  ModelPrice: string
  ModelRatio: string
  CacheRatio: string
  CreateCacheRatio: string
  CompletionRatio: string
  ImageRatio: string
  AudioRatio: string
  AudioCompletionRatio: string
  BillingMode: string
  BillingExpr: string
}

const numericFields = {
  ModelPrice: 'price',
  ModelRatio: 'ratio',
  CacheRatio: 'cacheRatio',
  CreateCacheRatio: 'createCacheRatio',
  CompletionRatio: 'completionRatio',
  ImageRatio: 'imageRatio',
  AudioRatio: 'audioRatio',
  AudioCompletionRatio: 'audioCompletionRatio',
} as const

// Build one snapshot before any React/form update. Invalid JSON must not turn
// an existing price map into an empty map, and an explicit zero is a price.
export function mergePricingDraft<T extends PricingMaps>(
  base: T,
  data: ModelRatioData,
  targets = [data.name]
): T {
  const next = { ...base }
  const name = data.name.trim()
  if (!name) throw new Error('Model name is required')
  const maps = {} as Record<keyof PricingMaps, Record<string, number | string>>
  for (const field of [
    ...Object.keys(numericFields),
    'BillingMode',
    'BillingExpr',
  ] as Array<keyof PricingMaps>) {
    const value: unknown = JSON.parse(base[field] || '{}')
    if (!value || typeof value !== 'object' || Array.isArray(value)) {
      throw new Error('Invalid model pricing JSON')
    }
    maps[field] = { ...value } as Record<string, number | string>
  }
  const renamed =
    targets.length === 1 &&
    targets[0] === data.name &&
    data.originalName &&
    data.originalName !== name
  if (renamed && Object.values(maps).some((map) => Object.hasOwn(map, name))) {
    throw new Error('A model with this name already exists')
  }
  for (const rawName of targets) {
    const target = rawName.trim()
    for (const map of Object.values(maps)) {
      delete map[target]
      if (renamed) delete map[data.originalName!]
    }
    for (const [field, source] of Object.entries(numericFields)) {
      if (
        data.billingMode !== 'tiered_expr' &&
        data.price !== undefined &&
        data.price !== '' &&
        field !== 'ModelPrice'
      )
        continue
      const raw = data[source as keyof typeof data]
      if (raw === undefined || raw === '') continue
      const value = Number(raw)
      if (!Number.isFinite(value) || value < 0)
        throw new Error('Prices must be finite non-negative numbers')
      maps[field as keyof PricingMaps][target] = value
    }
    if (data.billingMode === 'tiered_expr') {
      const expression = combineBillingExpr(
        data.billingExpr || '',
        data.requestRuleExpr || ''
      )
      if (!expression) throw new Error('Billing expression is required')
      maps.BillingMode[target] = 'tiered_expr'
      maps.BillingExpr[target] = expression
    }
  }
  for (const field of Object.keys(maps) as Array<keyof PricingMaps>) {
    next[field] = JSON.stringify(maps[field], null, 2)
  }
  return next
}
