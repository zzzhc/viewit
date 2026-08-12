// 主题状态:system(跟随系统) / light / dark。
// 实际生效主题挂在 <html> 的 .dark 类上,index.html 内联脚本在首帧前
// 完成初始化以避免闪烁;这里负责后续切换与系统偏好监听。
const KEY = 'viewit-theme'
export const THEMES = ['system', 'light', 'dark']
const THEME_LABELS = { system: '跟随系统', light: '浅色', dark: '深色' }

let pref = $state(load())
let systemDark = $state(false)

function load() {
  try {
    const v = localStorage.getItem(KEY)
    return THEMES.includes(v) ? v : 'system'
  } catch {
    return 'system'
  }
}

function apply() {
  document.documentElement.classList.toggle('dark', isDark())
}

export function initTheme() {
  const mq = window.matchMedia('(prefers-color-scheme: dark)')
  const onChange = () => {
    systemDark = mq.matches
    apply()
  }
  mq.addEventListener('change', onChange)
  systemDark = mq.matches
  apply()
  return () => mq.removeEventListener('change', onChange)
}

export function isDark() {
  return pref === 'dark' || (pref === 'system' && systemDark)
}

export function themePref() {
  return pref
}

export function themeLabel() {
  return THEME_LABELS[pref]
}

export function cycleTheme() {
  pref = THEMES[(THEMES.indexOf(pref) + 1) % THEMES.length]
  try {
    localStorage.setItem(KEY, pref)
  } catch {
    // localStorage 不可用时主题仅在本次会话生效
  }
  apply()
}
