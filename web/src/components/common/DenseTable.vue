<template>
  <section class="panel table-wrap">
    <slot />
  </section>
</template>

<style scoped>
.table-wrap {
  overflow: auto;
  border-radius: var(--radius-sm);
  border: 1px solid rgba(148, 163, 184, 0.25);
  background: #ffffff;
}

:slotted(table) {
  width: 100%;
  min-width: 760px;
}

@media (max-width: 920px) {
  :slotted(table) {
    min-width: 620px;
  }
}
</style>

