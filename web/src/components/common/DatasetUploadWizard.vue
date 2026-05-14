<template>
  <section v-if="open" class="wizard-backdrop">
    <article class="panel wizard">
      <header>
        <div>
          <strong>資料集匯入精靈</strong>
          <p>送到後端前，先在本機驗證 CSV。</p>
        </div>
        <button class="btn ghost" @click="$emit('close')">關閉</button>
      </header>
      <ol class="steps">
        <li v-for="(stepLabel, index) in steps" :key="stepLabel" :class="{ active: index === step }">{{ stepLabel }}</li>
      </ol>
      <div class="grid-gap">
        <label>
          CSV 檔案
          <input class="control" type="file" accept=".csv,text/csv" @change="onFileSelected" />
        </label>
        <div v-if="fileName" class="panel preview">
          <strong>{{ fileName }}</strong>
          <p>{{ validationMessage }}</p>
          <pre v-if="previewRows.length">{{ previewRows.join('\n') }}</pre>
        </div>
      </div>
      <footer>
        <button class="btn primary" :disabled="!file || loading || !isValid" @click="submitImport">
          {{ loading ? '匯入中…' : '確認匯入' }}
        </button>
      </footer>
    </article>
  </section>
</template>

<script setup lang="ts">
import { ref } from 'vue'

const props = defineProps<{ open: boolean; loading?: boolean }>()
const emit = defineEmits<{ close: []; submit: [File] }>()

const steps = ['上傳', '格式檢查', '預覽', '確認']
const step = ref(0)
const file = ref<File | null>(null)
const fileName = ref('')
const previewRows = ref<string[]>([])
const validationMessage = ref('選擇 CSV 檔案開始驗證。')
const isValid = ref(false)

async function onFileSelected(event: Event) {
  const target = event.target as HTMLInputElement
  const selected = target.files?.[0] ?? null
  file.value = selected
  fileName.value = selected?.name ?? ''
  previewRows.value = []
  isValid.value = false
  step.value = 0

  if (!selected) {
    validationMessage.value = '選擇 CSV 檔案開始驗證。'
    return
  }

  if (selected.size > 10 * 1024 * 1024) {
    validationMessage.value = '目前介面限制匯入檔案必須小於 10MB。'
    return
  }

  const text = await selected.text()
  const lines = text.split(/\r?\n/).filter(Boolean)
  const header = lines[0]?.trim().toLowerCase()
  if (header !== 'timestamp,open,high,low,close,volume') {
    validationMessage.value = 'CSV 表頭必須是：timestamp,open,high,low,close,volume'
    step.value = 1
    return
  }

  previewRows.value = lines.slice(0, 10)
  validationMessage.value = `本機已驗證 ${Math.max(0, lines.length - 1)} 筆資料列，後端仍會再次驗證。`
  isValid.value = true
  step.value = 3
}

function submitImport() {
  if (!file.value || !isValid.value || props.loading) {
    return
  }
  emit('submit', file.value)
}
</script>

<style scoped>
.wizard-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(3, 8, 20, 0.7);
  display: grid;
  place-items: center;
  padding: 1.25rem;
  z-index: 30;
}
.wizard {
  width: min(760px, 100%);
  padding: 1rem;
}
header, footer {
  display: flex;
  justify-content: space-between;
  gap: 1rem;
  align-items: center;
}
header p {
  margin: 0.35rem 0 0;
  color: var(--muted);
}
.steps {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 0.6rem;
  padding: 0;
  list-style: none;
  margin: 1rem 0;
}
.steps li {
  padding: 0.6rem 0.75rem;
  border-radius: 999px;
  background: rgba(255, 255, 255, 0.05);
  color: var(--muted);
  text-align: center;
}
.steps li.active {
  color: var(--text);
  background: var(--accent-soft);
}
.preview {
  padding: 0.9rem;
}
.preview p {
  color: var(--muted);
}
pre {
  white-space: pre-wrap;
  margin: 0;
  color: #d9e4ff;
}
</style>
