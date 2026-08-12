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

// 深色主题，颜色取自 app.css 的 --bg/--bg-panel/--fg 变量值
const darkTheme = EditorView.theme({
  '&': { backgroundColor: '#1e1e1e', color: '#d4d4d4', height: '100%', fontSize: '13px' },
  '.cm-content': { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace', caretColor: '#d4d4d4' },
  '.cm-gutters': { backgroundColor: '#252526', color: '#6e7681', borderRight: '1px solid #333' },
  '.cm-activeLine': { backgroundColor: 'rgba(255,255,255,0.04)' },
  '.cm-activeLineGutter': { backgroundColor: 'rgba(255,255,255,0.04)', color: '#d4d4d4' },
  '&.cm-focused': { outline: 'none' },
  '.cm-selectionBackground, &.cm-focused .cm-selectionBackground': { backgroundColor: '#264f78' },
  '.cm-cursor': { borderLeftColor: '#d4d4d4' },
  '.cm-foldPlaceholder': { backgroundColor: '#333', border: 'none', color: '#9d9d9d' },
  '.cm-tooltip': { backgroundColor: '#252526', border: '1px solid #333', color: '#d4d4d4' },
  '.cm-xml-match': { backgroundColor: 'rgba(79,140,255,0.35)' },
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
      darkTheme,
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
