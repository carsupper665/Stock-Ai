export function formatCurrency(value?: number | null, currency = 'USD') {
  if (value === null || value === undefined || Number.isNaN(value)) {
    return '--'
  }
  return new Intl.NumberFormat(undefined, {
    style: 'currency',
    currency,
    maximumFractionDigits: 2,
  }).format(value)
}

export function formatNumber(value?: number | null, digits = 2) {
  if (value === null || value === undefined || Number.isNaN(value)) {
    return '--'
  }
  return new Intl.NumberFormat(undefined, {
    maximumFractionDigits: digits,
    minimumFractionDigits: 0,
  }).format(value)
}

export function formatDateTime(value?: string | null) {
  if (!value) {
    return '--'
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime()) || date.getUTCFullYear() <= 1) {
    return '--'
  }
  return date.toLocaleString()
}

export function formatPercent(value?: number | null) {
  if (value === null || value === undefined || Number.isNaN(value)) {
    return '--'
  }
  return `${value.toFixed(2)}%`
}

export function splitSymbols(value?: string) {
  if (!value) {
    return []
  }
  return value
    .split(',')
    .map((item) => item.trim().toUpperCase())
    .filter(Boolean)
}

export function joinSymbols(values?: string[] | null) {
  return values?.join(', ') ?? ''
}

export function toLocalDateTimeInput(value?: string | null) {
  if (!value) {
    return ''
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime()) || date.getUTCFullYear() <= 1) {
    return ''
  }
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60000)
  return local.toISOString().slice(0, 16)
}

export function fromLocalDateTimeInput(value: string) {
  if (!value) {
    return ''
  }
  const local = new Date(value)
  if (Number.isNaN(local.getTime())) {
    return ''
  }
  return new Date(local.getTime() + local.getTimezoneOffset() * 60000).toISOString()
}
