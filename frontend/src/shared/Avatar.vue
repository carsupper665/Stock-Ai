<script setup>
// 頭像：目前是空的灰色圓形，之後換成圖片只改這個元件。status 是成員狀態點。
defineProps({ size: { type: Number, default: 40 }, status: { type: String, default: null } })
const labels = { active: '正在執行 Run', waiting: '等待下一個 Run', stopped: 'Session 已停止' }
</script>

<template>
  <span class="avatar-circle" :style="{ width: `${size}px`, height: `${size}px` }">
    <span v-if="status" class="status" :data-status="status" :title="labels[status]" role="img" :aria-label="labels[status]"></span>
  </span>
</template>

<style scoped>
.avatar-circle { position: relative; display: block; flex: none; border-radius: 50%; background: var(--surf3); }
.status { position: absolute; right: 0; bottom: 0; width: 28%; height: 28%; min-width: 10px; min-height: 10px; border-radius: 50%; border: 2px solid var(--surf); box-sizing: content-box; background: var(--mute); }
.status[data-status='active'] { background: var(--pos); }
/* 月牙：透明圓加上偏移的內陰影 */
.status[data-status='waiting'] { background: var(--surf); box-shadow: inset -4px -2px 0 0 var(--warn); transform: rotate(-30deg); }
</style>
