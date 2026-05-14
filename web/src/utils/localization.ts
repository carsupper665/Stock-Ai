const statusLabels: Record<string, string> = {
  active: '啟用',
  canceled: '已取消',
  completed: '已完成',
  connected: '已連線',
  degraded: '降級',
  disabled: '停用',
  disconnected: '已斷線',
  draft: '草稿',
  failed: '失敗',
  filled: '已成交',
  healthy: '健康',
  info: '資訊',
  lagging: '延遲',
  missing: '缺失',
  new: '新建',
  paused: '已暫停',
  pending: '等待中',
  queued: '排隊中',
  ready: '就緒',
  rejected: '已拒絕',
  running: '運行中',
  stale: '過期',
  stopped: '已停止',
  unknown: '未知',
  warning: '警告',
}

const sideLabels: Record<string, string> = {
  buy: '買入',
  sell: '賣出',
  long: '多單',
  short: '空單',
}

const eventTopicLabels: Record<string, string> = {
  'dataset.import.completed': '資料集匯入完成',
  'dataset.import.failed': '資料集匯入失敗',
  'order.created': '訂單已建立',
  'order.rejected': '訂單已拒絕',
  'sandbox.lifecycle': '沙盒生命週期事件',
  'sandbox.replay.seek': '回放時間已跳轉',
  'trade.executed': '交易已成交',
}

export function formatStatusLabel(label?: string | null) {
  if (!label) {
    return '未知'
  }
  return statusLabels[label.toLowerCase()] ?? label
}

export function formatSideLabel(label?: string | null) {
  if (!label) {
    return '--'
  }
  return sideLabels[label.toLowerCase()] ?? label
}

export function formatEventTopic(topic?: string | null) {
  if (!topic) {
    return '事件'
  }
  return eventTopicLabels[topic] ?? topic
}
