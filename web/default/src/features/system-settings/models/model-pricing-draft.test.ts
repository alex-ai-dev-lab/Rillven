/* Copyright (C) 2026 Rillven contributors. SPDX-License-Identifier: AGPL-3.0-or-later */
import { describe, expect, it } from 'bun:test'
import { mergePricingDraft, type PricingMaps } from './model-pricing-draft'

const base = (): PricingMaps => ({
  ModelPrice: '{}',
  ModelRatio: '{"old":2,"other":3}',
  CacheRatio: '{"old":0.5}',
  CreateCacheRatio: '{}',
  CompletionRatio: '{}',
  ImageRatio: '{}',
  AudioRatio: '{}',
  AudioCompletionRatio: '{}',
  BillingMode: '{}',
  BillingExpr: '{}',
})

describe('pricing save snapshot', () => {
  it('includes the open editor, preserves zero and leaves unrelated models untouched', () => {
    const before = base()
    const saved = mergePricingDraft(before, {
      name: 'old',
      billingMode: 'per-request',
      price: '0',
    })
    expect(JSON.parse(saved.ModelPrice)).toEqual({ old: 0 })
    expect(JSON.parse(saved.ModelRatio)).toEqual({ other: 3 })
    expect(JSON.parse(saved.CacheRatio)).toEqual({})
    expect(before).toEqual(base())
  })
  it('renames all pricing lanes atomically and refuses to overwrite another model', () => {
    const saved = mergePricingDraft(base(), {
      name: 'new',
      originalName: 'old',
      ratio: '4',
    })
    expect(JSON.parse(saved.ModelRatio)).toEqual({ new: 4, other: 3 })
    expect(JSON.parse(saved.CacheRatio)).toEqual({})
    expect(() =>
      mergePricingDraft(base(), {
        name: 'other',
        originalName: 'old',
        ratio: '4',
      })
    ).toThrow()
  })
  it('does not replace invalid maps or invalid prices with successful empty settings', () => {
    expect(() =>
      mergePricingDraft(
        { ...base(), ModelRatio: '{' },
        { name: 'new', ratio: '2' }
      )
    ).toThrow()
    for (const ratio of ['Infinity', '-1', '4oops']) {
      expect(() => mergePricingDraft(base(), { name: 'new', ratio })).toThrow()
    }
  })
})
