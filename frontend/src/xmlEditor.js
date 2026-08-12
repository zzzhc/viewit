import { basicSetup } from 'codemirror'
import { EditorState, StateEffect, StateField } from '@codemirror/state'
import { Decoration, EditorView } from '@codemirror/view'
import { xml } from '@codemirror/lang-xml'

const setMatch = StateEffect.define() // 持久 XPath 命中高亮 [[from,to],...]
const setFlash = StateEffect.define() // 临时点击闪烁 [[from,to],...]

function deco(ranges, cls) {
  return Decoration.set(ranges.map(([from, to]) => Decoration.mark({ class: cls }).range(from, to)))
}

function rangeField(cls, effect) {
  return StateField.define({
    create: () => Decoration.none,
    update(d, tr) {
      d = d.map(tr.changes)
      for (const e of tr.effects) if (e.is(effect)) d = deco(e.value, cls)
      return d
    },
    provide: (f) => EditorView.decorations.from(f)
  })
}

const matchField = rangeField('cm-xml-match', setMatch)
const flashField = rangeField('cm-xml-flash', setFlash)

// 主题随 app.css 的 --vt-* 变量,深浅主题自动切换
const theme = EditorView.theme({
  '&': { backgroundColor: 'var(--vt-bg)', color: 'var(--vt-fg)', height: '100%', fontSize: '13px' },
  '.cm-content': { fontFamily: 'var(--font-mono)', caretColor: 'var(--vt-fg)' },
  '.cm-gutters': { backgroundColor: 'var(--vt-panel)', color: 'var(--vt-muted)', borderRight: '1px solid var(--vt-edge)' },
  '.cm-activeLine': { backgroundColor: 'var(--vt-hover)' },
  '.cm-activeLineGutter': { backgroundColor: 'var(--vt-hover)', color: 'var(--vt-fg)' },
  '&.cm-focused': { outline: 'none' },
  '.cm-selectionBackground, &.cm-focused .cm-selectionBackground': { backgroundColor: 'var(--vt-selection)' },
  '.cm-cursor': { borderLeftColor: 'var(--vt-fg)' },
  '.cm-foldPlaceholder': { backgroundColor: 'var(--vt-panel)', border: 'none', color: 'var(--vt-muted)' },
  '.cm-tooltip': { backgroundColor: 'var(--vt-panel)', border: '1px solid var(--vt-edge)', color: 'var(--vt-fg)' },
  '.cm-xml-match': { backgroundColor: 'var(--vt-selection-soft)' },
  '.cm-xml-flash': { backgroundColor: 'rgba(240,200,60,0.4)' }
}, { dark: true })

export function createXmlView(parent, doc) {
  const state = EditorState.create({
    doc,
    extensions: [
      basicSetup, // 行号、折叠、Ctrl-F 搜索、括号匹配等
      xml(),
      matchField,
      flashField,
      theme,
      EditorState.readOnly.of(true),
      EditorView.editable.of(false)
    ]
  })
  const view = new EditorView({ state, parent })
  let flashTimer = null
  return {
    destroy() {
      clearTimeout(flashTimer)
      view.destroy()
    },
    setMatches(ranges) {
      view.dispatch({ effects: setMatch.of(ranges) })
    },
    reveal(from, to = from + 1) {
      view.dispatch({
        selection: { anchor: from },
        effects: [EditorView.scrollIntoView(from, { y: 'center' }), setFlash.of([[from, to]])]
      })
      if (flashTimer) clearTimeout(flashTimer)
      flashTimer = setTimeout(() => {
        view.dispatch({ effects: setFlash.of([]) })
        flashTimer = null
      }, 1200)
    }
  }
}
