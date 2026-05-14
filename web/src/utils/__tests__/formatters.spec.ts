import { describe, expect, it } from 'vitest'
import { formatDateTime } from '@/utils/formatters'

describe('formatDateTime', () => {
  it('returns a placeholder for zero-value timestamps from the backend', () => {
    expect(formatDateTime('0001-01-01T00:00:00Z')).toBe('--')
  })

  it('formats valid timestamps for display', () => {
    expect(formatDateTime('2025-01-01T00:00:00Z')).not.toBe('--')
  })
})
