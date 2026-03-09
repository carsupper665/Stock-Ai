<template>
  <section class="panel table-wrap">
    <slot />
  </section>
</template>

<style scoped>
.table-wrap {
  overflow: auto;
  padding: 0.35rem;
}
:slotted(table) {
  width: 100%;
}
</style>
