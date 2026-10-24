<script setup>
import { nextTick, onUnmounted, ref } from 'vue'

import Avatar from '../../shared/Avatar.vue'

defineProps({ members: Array, unbound: Array })
const emit = defineEmits(['open'])

const menu = ref(null)
const menuEl = ref(null)
const MENU_WIDTH = 236

async function openMenu(member, event) {
  menu.value = { member, x: Math.max(8, Math.min(event.clientX, innerWidth - MENU_WIDTH - 8)), y: event.clientY }
  await nextTick()
  const height = menuEl.value?.offsetHeight ?? 0
  if (menu.value && menu.value.y + height > innerHeight - 8) menu.value.y = Math.max(8, innerHeight - height - 8)
  menuEl.value?.querySelector('[role=menuitem]')?.focus()
}
const closeMenu = () => { menu.value = null }
function manage() {
  const { member } = menu.value
  closeMenu()
  emit('open', member)
}
const onKey = (event) => { if (event.key === 'Escape') closeMenu() }
addEventListener('keydown', onKey)
onUnmounted(() => removeEventListener('keydown', onKey))
</script>

<template>
  <div class="member-list">
    <button v-for="member in members" :key="member.id" type="button" class="member" @click="emit('open', member)" @contextmenu.prevent="openMenu(member, $event)">
      <Avatar :size="48" :status="member.status" />
      <span class="names">
        <b class="ellipsis">{{ member.name }}</b>
        <span class="sub ellipsis">{{ member.sub }}</span>
      </span>
    </button>
    <p v-if="!members.length && !unbound.length" class="note none">還沒有交易帳戶。</p>
    <details v-if="unbound.length" class="unbound">
      <summary>未綁 Session（{{ unbound.length }}）</summary>
      <button v-for="member in unbound" :key="member.id" type="button" class="member" @click="emit('open', member)" @contextmenu.prevent="openMenu(member, $event)">
        <Avatar :size="48" />
        <span class="names"><b class="ellipsis">{{ member.name }}</b></span>
      </button>
    </details>

    <template v-if="menu">
      <div class="backdrop" @click="closeMenu" @contextmenu.prevent="closeMenu"></div>
      <div ref="menuEl" class="menu" role="menu" :aria-label="`${menu.member.name}的管理選單`" :style="{ left: `${menu.x}px`, top: `${menu.y}px`, width: `${MENU_WIDTH}px` }">
        <div class="menu-head">
          <Avatar :size="40" :status="menu.member.status" />
          <span class="names"><b class="ellipsis">{{ menu.member.name }}</b><span v-if="menu.member.sub" class="sub ellipsis">{{ menu.member.sub }}</span></span>
        </div>
        <button type="button" role="menuitem" class="menu-item" @click="manage">管理使用者</button>
      </div>
    </template>
  </div>
</template>

<style scoped>
.member-list { display: flex; flex-direction: column; min-width: 0; }
.member { width: 100%; min-height: 68px; border: 0; border-radius: 0; background: transparent; padding: 10px 16px; display: flex; align-items: center; gap: 12px; text-align: left; color: var(--ink); font-size: 14px; white-space: normal; }
.member:hover:not(:disabled) { background: var(--surf2); }
.names { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.names b { font-weight: 700; font-size: 14px; }
.sub { font-size: 12px; color: var(--dim); }
.none { padding: 16px; }
.unbound summary { padding: 10px 16px 6px; font-size: 13px; font-weight: 700; color: var(--mute); cursor: pointer; }
.backdrop { position: fixed; inset: 0; z-index: 40; }
.menu { position: fixed; z-index: 41; max-height: calc(100vh - 16px); overflow-y: auto; background: var(--surf); border: 1px solid var(--line); border-radius: 14px; padding: 6px; box-shadow: var(--modal-shadow); }
.menu-head { display: flex; align-items: center; gap: 10px; padding: 8px 8px 10px; border-bottom: 1px solid var(--line); margin-bottom: 6px; }
.menu-item { width: 100%; min-height: 44px; border: 0; border-radius: 10px; padding: 0 10px; text-align: left; font-size: 15px; color: var(--ink); }
.menu-item:hover:not(:disabled) { background: var(--surf2); }
</style>
