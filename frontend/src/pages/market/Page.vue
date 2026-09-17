<script setup>
import { onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { useLoad } from '../../core/load.js'
import { fmtTime, num, tone } from '../../shared/format.js'
import { getPrice, listSubscriptions } from './api.js'

const route = useRoute()
const router = useRouter()

const market = ref(route.query.market ?? 'crypto')
const draft = ref(route.query.symbol ?? 'BTCUSDT')
const symbol = ref(route.query.symbol ?? '')

// 查價會讓後端啟動該 symbol 的訂閱；有查過就每 5 秒輪詢，訂閱表一起更新
const quote = useLoad(() => (symbol.value ? getPrice(market.value, symbol.value) : null))
const subs = useLoad(() => listSubscriptions().then((r) => r.subscriptions))
const timer = setInterval(() => {
  if (symbol.value) quote.reload()
  subs.reload()
}, 5000)
onUnmounted(() => clearInterval(timer))

function ask() {
  symbol.value = draft.value.trim().toUpperCase()
  router.replace({ query: { market: market.value, symbol: symbol.value } })
  quote.reload()
}
</script>

<template>
  <article>
    <div class="cols">
      <section class="card col quote">
        <div class="eyebrow">查價</div>
        <form class="row" @submit.prevent="ask">
          <span class="chips">
            <button type="button" :aria-pressed="market === 'crypto'" @click="market = 'crypto'">crypto</button>
            <button type="button" :aria-pressed="market === 'stock'" @click="market = 'stock'">stock · 501</button>
          </span>
          <input v-model="draft" class="mono symbol" placeholder="BTCUSDT" required />
          <button data-tone="acc" :disabled="quote.loading.value">查價</button>
        </form>
        <div class="inset col result">
          <p v-if="quote.error.value" data-tone="neg">
            {{ quote.error.value.message }}
            <span v-if="['market_unavailable', 'market_timeout'].includes(quote.error.value.code)" class="note">（可重試：5 秒後自動再查）</span>
          </p>
          <template v-else-if="quote.data.value">
            <div class="row">
              <span class="mono big">{{ num(quote.data.value.price) }}</span>
              <span class="mono" data-tone="mute">{{ quote.data.value.market }}:{{ quote.data.value.symbol }}</span>
            </div>
            <span class="mono" data-tone="mute">updated_at {{ fmtTime(quote.data.value.updated_at) }} · 每 5 秒輪詢</span>
          </template>
          <span v-else class="note">輸入 symbol 後查價</span>
        </div>
        <p class="hint">查價會讓後端啟動該 symbol 的訂閱；<code>stock</code> 目前回 <code>not_implemented</code>（501）；<code>market_unavailable</code>／<code>market_timeout</code> 可重試。</p>
      </section>

      <section class="card col subs">
        <div class="row"><span class="eyebrow">訂閱狀態</span><span class="note">營運資訊，不是交易資訊 · 60 秒無人要求即 inactive · 每 5 秒更新</span></div>
        <p v-if="subs.error.value" data-tone="neg">{{ subs.error.value.message }}</p>
        <div v-else-if="subs.data.value" class="rows">
          <div v-for="(state, key) in subs.data.value" :key="key" class="row sub">
            <i class="dot square" :class="{ blip: state === 'activating' }" :data-tone="tone(state)"></i>
            <span class="mono grow">{{ key }}</span>
            <span class="mono state" :data-tone="tone(state)">{{ state }}</span>
          </div>
          <p v-if="!Object.keys(subs.data.value).length" class="note">目前沒有任何訂閱；查一次價就會出現。</p>
        </div>
        <p v-else class="note">載入中…</p>
      </section>
    </div>
  </article>
</template>

<style scoped>
.quote {
  flex: 1 1 340px;
  gap: 10px;
}
.subs {
  flex: 1 1 380px;
  gap: 6px;
}
.symbol {
  width: 130px;
}
.result {
  gap: 5px;
}
.sub {
  gap: 9px;
  flex-wrap: nowrap;
}
.state {
  width: 78px;
  text-align: right;
}
</style>
