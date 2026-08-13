import hljs from 'highlight.js'
import postscript from './postscript.js'

// `highlight.js` is imported as the full bundle, which registers every
// bundled language; postscript is the only grammar supplied by this project.
hljs.registerLanguage('postscript', postscript)

// Browsers can actually render these. HEIC/HEIF/TIFF deliberately excluded:
// Chrome does not decode them in <img>, a broken image is worse than download.
export const IMAGE = ['png', 'jpg', 'jpeg', 'jfif', 'gif', 'webp', 'svg', 'bmp', 'avif', 'apng', 'ico']
export const VIDEO = ['mp4', 'webm', 'ogv', 'mov', 'm4v']
export const AUDIO = ['mp3', 'wav', 'ogg', 'flac', 'm4a', 'aac', 'opus', 'weba']
export const PDF = ['pdf']
// Both extensions (txt, log, ...) and full names (license, dockerignore, ...)
export const TEXT = [
  'txt', 'log', 'csv', 'tsv', 'text',
  'gitignore', 'gitattributes', 'editorconfig', 'procfile', 'dockerignore',
  'license', 'licence', 'readme', 'changelog', 'copying', 'notice', 'authors', 'todo'
]

// Full filename first, then extension. Values are hljs language names; the
// full bundle registers every one of them, so none silently degrades to
// plaintext (codeLanguage() still guards against a stale name).
const CODE_LANG = {
  // systems / compiled
  go: 'go',
  rs: 'rust',
  c: 'c', h: 'c',
  cc: 'cpp', cpp: 'cpp', cxx: 'cpp', 'c++': 'cpp', hpp: 'cpp', hh: 'cpp', hxx: 'cpp', 'h++': 'cpp',
  cs: 'csharp',
  java: 'java',
  kt: 'kotlin', kts: 'kotlin',
  swift: 'swift',
  d: 'd',
  nim: 'nim',
  cr: 'crystal',
  m: 'objectivec', mm: 'objectivec', // .m is Objective-C (the common convention)
  pas: 'delphi', dpr: 'delphi', dpk: 'delphi',
  pp: 'puppet', // Puppet manifest, not Pascal (that's .pas)
  bas: 'basic',
  ada: 'ada', adb: 'ada', ads: 'ada',
  vala: 'vala',
  hx: 'haxe',
  f: 'fortran', f90: 'fortran', f95: 'fortran', f03: 'fortran', for: 'fortran',
  vb: 'vbnet',
  vbs: 'vbscript',
  fs: 'fsharp', fsx: 'fsharp', fsi: 'fsharp',
  ml: 'ocaml', mli: 'ocaml',
  sml: 'sml',
  hs: 'haskell', lhs: 'haskell',
  erl: 'erlang', hrl: 'erlang',
  ex: 'elixir', exs: 'elixir',
  clj: 'clojure', cljs: 'clojure', cljc: 'clojure', edn: 'clojure',
  scm: 'scheme', ss: 'scheme', rkt: 'scheme',
  lisp: 'lisp', lsp: 'lisp', el: 'lisp',
  pro: 'prolog',
  // scripting
  js: 'javascript', jsx: 'javascript', mjs: 'javascript', cjs: 'javascript',
  ts: 'typescript', tsx: 'typescript', mts: 'typescript', cts: 'typescript',
  py: 'python', pyw: 'python', pyi: 'python', pyx: 'python', pxd: 'python',
  rb: 'ruby', rake: 'ruby', gemspec: 'ruby', ru: 'ruby',
  php: 'php', php3: 'php', php4: 'php', php5: 'php', phtml: 'php',
  pl: 'perl', pm: 'perl', pod: 'perl', t: 'perl',
  lua: 'lua',
  r: 'r',
  scala: 'scala', sc: 'scala',
  groovy: 'groovy',
  dart: 'dart',
  elm: 'elm',
  coffee: 'coffeescript',
  ls: 'livescript',
  moon: 'moonscript',
  ps1: 'powershell', psm1: 'powershell', psd1: 'powershell',
  bat: 'dos', cmd: 'dos',
  sh: 'bash', bash: 'bash', zsh: 'bash', ksh: 'bash', fish: 'shell',
  awk: 'awk',
  tcl: 'tcl',
  // markup / stylesheets
  html: 'xml', htm: 'xml', xhtml: 'xml',
  xml: 'xml', xsd: 'xml', xsl: 'xml', xslt: 'xml', svg: 'xml', rss: 'xml', atom: 'xml', plist: 'xml', pom: 'xml', wsdl: 'xml',
  css: 'css',
  scss: 'scss', sass: 'scss',
  less: 'less',
  styl: 'stylus', stylus: 'stylus',
  vue: 'xml', svelte: 'xml', // no vue/svelte grammar; xml covers the template
  twig: 'twig',
  hbs: 'handlebars', handlebars: 'handlebars', mustache: 'handlebars',
  haml: 'haml',
  erb: 'erb', rhtml: 'erb',
  md: 'markdown', markdown: 'markdown', mdx: 'markdown',
  adoc: 'asciidoc', asciidoc: 'asciidoc',
  tex: 'latex', latex: 'latex',
  // data / config
  json: 'json', jsonc: 'json', map: 'json', ipynb: 'json', geojson: 'json',
  yaml: 'yaml', yml: 'yaml',
  ini: 'ini', conf: 'ini', cfg: 'ini', env: 'ini',
  toml: 'ini', // no toml grammar in hljs; ini covers [section]/key=value
  properties: 'properties',
  sql: 'sql',
  pgsql: 'pgsql',
  graphql: 'graphql', gql: 'graphql',
  proto: 'protobuf',
  thrift: 'thrift',
  capnp: 'capnproto',
  nix: 'nix',
  dockerfile: 'dockerfile',
  makefile: 'makefile', mk: 'makefile', mak: 'makefile',
  cmake: 'cmake',
  gradle: 'gradle',
  vim: 'vim',
  ldif: 'ldif',
  diff: 'diff', patch: 'diff',
  http: 'http',
  ps: 'postscript', eps: 'postscript',
  // graphics / hardware / scientific
  glsl: 'glsl', vert: 'glsl', frag: 'glsl', geom: 'glsl', comp: 'glsl',
  jl: 'julia',
  v: 'verilog', vh: 'verilog', sv: 'verilog', svh: 'verilog',
  vhd: 'vhdl', vhdl: 'vhdl',
  asm: 'x86asm', s: 'x86asm',
  ll: 'llvm',
  wat: 'wasm',
  gcode: 'gcode',
  ino: 'arduino',
  pde: 'processing',
  as: 'actionscript',
  ahk: 'autohotkey',
  au3: 'autoit',
  applescript: 'applescript', scpt: 'applescript',
  // dotted / extension-less well-known names
  'go.mod': 'plaintext',
  'go.sum': 'plaintext',
  'go.work': 'plaintext',
  'cargo.lock': 'ini',
  '.gitmodules': 'ini',
  '.npmrc': 'ini',
  '.yarnrc': 'ini',
  '.bashrc': 'bash',
  '.bash_profile': 'bash',
  '.bash_aliases': 'bash',
  '.profile': 'bash',
  '.zshrc': 'bash',
  '.vimrc': 'vim',
  'nginx.conf': 'nginx',
  '.htaccess': 'apache',
  'httpd.conf': 'apache',
  'apache2.conf': 'apache',
  gemfile: 'ruby',
  rakefile: 'ruby',
  vagrantfile: 'ruby',
  jenkinsfile: 'groovy',
  'cmakelists.txt': 'cmake'
}

export function extension(name) {
  const i = name.lastIndexOf('.')
  return i >= 0 ? name.slice(i + 1).toLowerCase() : ''
}

// Returns an hljs language name, or 'plaintext' when the mapping resolves but
// the language is not bundled, or null when the file is not code at all.
// .gz is transparent app-wide, so it is stripped before the lookup.
export function codeLanguage(name) {
  const lower = name.toLowerCase().replace(/\.gz$/, '')
  const lang = CODE_LANG[lower] || CODE_LANG[extension(lower)]
  if (!lang) return null
  return hljs.getLanguage(lang) ? lang : 'plaintext'
}

// Decide the viewer from the content-sniffed mime (authoritative) plus the
// filename as a fallback hint. The sniffer cannot see webm/avif/ico/ogg-style
// containers, so known media extensions are trusted only when the content
// itself came back unidentified (application/octet-stream, application/ogg,
// or missing). Everything else the sniffer identifies as binary lands in the
// download view no matter what the file is named.
// Display names for the type badge shown next to filenames.
const LANG_LABELS = {
  go: 'Go',
  json: 'JSON',
  markdown: 'Markdown',
  yaml: 'YAML',
  typescript: 'TypeScript',
  javascript: 'JavaScript',
  python: 'Python',
  rust: 'Rust',
  bash: 'Bash',
  shell: 'Shell',
  xml: 'XML',
  css: 'CSS',
  html: 'HTML',
  sql: 'SQL',
  ini: 'INI',
  c: 'C',
  cpp: 'C++',
  csharp: 'C#',
  java: 'Java',
  ruby: 'Ruby',
  php: 'PHP',
  dockerfile: 'Dockerfile',
  makefile: 'Makefile',
  powershell: 'PowerShell',
  protobuf: 'Protobuf',
  graphql: 'GraphQL',
  postscript: 'PostScript',
  objectivec: 'Objective-C',
  fsharp: 'F#',
  vbnet: 'VB.NET',
  vbscript: 'VBScript',
  x86asm: 'Assembly',
  autohotkey: 'AutoHotkey',
  autoit: 'AutoIt',
  applescript: 'AppleScript',
  actionscript: 'ActionScript',
  coffeescript: 'CoffeeScript',
  livescript: 'LiveScript',
  moonscript: 'MoonScript',
  ocaml: 'OCaml',
  sml: 'SML',
  matlab: 'MATLAB',
  latex: 'LaTeX',
  erb: 'ERB',
  dos: 'Batch',
  asciidoc: 'AsciiDoc',
  pgsql: 'PostgreSQL',
  http: 'HTTP',
  vhdl: 'VHDL',
  glsl: 'GLSL',
  gcode: 'G-code',
  llvm: 'LLVM',
  wasm: 'WebAssembly',
  ldif: 'LDIF',
  purebasic: 'PureBasic',
  plaintext: 'TEXT'
}

export function languageLabel(lang) {
  if (!lang || lang === 'plaintext') return 'TEXT'
  return LANG_LABELS[lang] || lang.charAt(0).toUpperCase() + lang.slice(1)
}

// Content-sniffed mimes that pin down a language without needing the filename.
const MIME_LANG = {
  'application/json': 'json',
  'application/xml': 'xml',
  'text/xml': 'xml',
  'text/html': 'xml',
  'text/css': 'css',
  'text/javascript': 'javascript',
  'application/javascript': 'javascript',
  'application/postscript': 'postscript'
}

export function languageFromMime(mime) {
  return MIME_LANG[(mime || '').split(';')[0].trim().toLowerCase()] || null
}

// Languages worth auto-detecting in plain text. Detecting against the full
// grammar set produces absurd results on small samples (a 4-line file came
// back as "Pgsql"); a curated list plus a minimum sample size keeps the
// fallback honest.
const AUTO_LANGS = [
  'json', 'yaml', 'xml', 'ini', 'markdown', 'bash', 'shell', 'python',
  'javascript', 'typescript', 'go', 'rust', 'java', 'c', 'cpp', 'csharp',
  'ruby', 'php', 'sql', 'css', 'scss', 'less', 'diff', 'dockerfile',
  'makefile', 'properties', 'gradle', 'groovy', 'kotlin', 'swift', 'lua',
  'perl', 'r', 'scala', 'powershell', 'protobuf', 'cmake', 'graphql',
  'dart', 'elixir', 'elm', 'haskell', 'clojure', 'erlang', 'ocaml',
  'fsharp', 'objectivec', 'fortran', 'nim', 'crystal', 'd', 'verilog',
  'vhdl', 'julia', 'matlab', 'latex', 'awk', 'tcl', 'delphi', 'prolog',
  'scheme', 'lisp', 'coffeescript', 'postscript', 'plaintext'
]

const MIN_AUTO_DETECT = 64 // shorter samples carry too little signal

// Interpreter basename (version/extension suffixes stripped) -> hljs language.
// A shebang is the strongest content signal: it names the interpreter.
const SHEBANG_LANG = {
  sh: 'bash', bash: 'bash', zsh: 'bash', ksh: 'bash', dash: 'bash', ash: 'bash', csh: 'bash', tcsh: 'bash',
  fish: 'shell',
  python: 'python',
  perl: 'perl',
  ruby: 'ruby',
  php: 'php',
  node: 'javascript', nodejs: 'javascript', bun: 'javascript',
  deno: 'typescript',
  lua: 'lua',
  rscript: 'r',
  awk: 'awk', gawk: 'awk', mawk: 'awk', nawk: 'awk',
  tclsh: 'tcl', wish: 'tcl', expect: 'tcl',
  escript: 'erlang',
  groovy: 'groovy',
  pwsh: 'powershell',
  crystal: 'crystal',
  elixir: 'elixir'
}

function languageFromShebang(text) {
  const m = /^#!\s*(\S+)([^\r\n]*)/.exec(text.slice(0, 256))
  if (!m) return null
  let prog = m[1].split('/').pop().toLowerCase()
  const rest = m[2].trim()
  if (prog === 'env') {
    // `#!/usr/bin/env python` (or `env -S python -u`): interpreter follows.
    const args = rest.split(/\s+/)
    let i = args[0] === '-S' || args[0] === '--split-string' ? 1 : 0
    while (i < args.length && args[i].startsWith('-')) i++
    prog = (args[i] || '').split('/').pop().toLowerCase()
  }
  prog = prog.replace(/\.exe$/i, '')
  if (SHEBANG_LANG[prog]) return SHEBANG_LANG[prog]
  return SHEBANG_LANG[prog.replace(/\d+(\.\d+)*$/, '')] || null // python3.11 -> python
}

export function detectLanguage(text) {
  const shebang = languageFromShebang(text)
  if (shebang) return shebang
  if (text.trim().length < MIN_AUTO_DETECT) return 'plaintext'
  const detected = hljs.highlightAuto(text.slice(0, 64 * 1024), AUTO_LANGS)
  return AUTO_LANGS.includes(detected.language) ? detected.language : 'plaintext'
}

// Copy text to the clipboard. The async Clipboard API only exists in secure
// contexts (https or localhost); on plain http LAN addresses fall back to the
// legacy textarea + execCommand('copy') path. Returns true on success.
export async function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // fall through to the legacy path
    }
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  ta.remove()
  return ok
}

export function viewerFor(name, mime) {
  // .gz 对前端透明:内层类型由剥掉 .gz 后的名字与服务器嗅探的内层 MIME
  // 决定(解压已在服务端完成)。前端不区分压缩与否。
  const lower = name.toLowerCase().replace(/\.gz$/, '')
  const ext = extension(lower)
  const mt = (mime || '').split(';')[0].trim().toLowerCase()

  if (mt.startsWith('image/')) return 'image'
  if (mt.startsWith('video/')) return 'video'
  if (mt.startsWith('audio/')) return 'audio'
  if (mt === 'application/pdf') return 'pdf'
  if (mt === 'application/xml' || mt === 'text/xml') return 'xml'
  if (mt === 'application/json') {
    if (ext === 'jsonl' || ext === 'jsonlines') return 'jsonl'
    return 'code'
  }
  if (mt.startsWith('text/')) {
    if (ext === 'md' || ext === 'markdown' || mt === 'text/markdown') return 'markdown'
    if (ext === 'jsonl' || ext === 'jsonlines') return 'jsonl'
    return 'code'
  }
  // PostScript is text the sniffer labels as application/postscript
  if (mt === 'application/postscript') return 'code'
  // any other identified mime is binary non-media: the filename must not
  // override what the content says (a .json holding a zip is a zip)
  if (mt && mt !== 'application/octet-stream' && mt !== 'application/ogg') return 'download'
  // unidentified content: media extension is the only hint the sniffer lacks
  if (IMAGE.includes(ext)) return 'image'
  if (VIDEO.includes(ext)) return 'video'
  if (AUDIO.includes(ext)) return 'audio'
  if (PDF.includes(ext)) return 'pdf'
  if (ext === 'html' || ext === 'htm') return 'html'
  if (ext === 'xml' || ext === 'xsd' || ext === 'xsl' || ext === 'xslt') return 'xml'
  if (ext === 'md' || ext === 'markdown') return 'markdown'
  if (ext === 'jsonl' || ext === 'jsonlines') return 'jsonl'
  if (codeLanguage(lower) !== null || TEXT.includes(ext) || TEXT.includes(lower)) return 'code'
  return 'download'
}
