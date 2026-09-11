export function setTheme(name) {
  // 'light' | 'dark' | ''（空字串＝跟系統）
  localStorage.setItem('theme', name)
  document.documentElement.dataset.theme = name
}

setTheme(localStorage.getItem('theme') ?? '')
