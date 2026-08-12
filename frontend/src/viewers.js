import hljs from 'highlight.js'
import cmake from 'highlight.js/lib/languages/cmake'
import gradle from 'highlight.js/lib/languages/gradle'
import groovy from 'highlight.js/lib/languages/groovy'
import powershell from 'highlight.js/lib/languages/powershell'
import protobuf from 'highlight.js/lib/languages/protobuf'
import scala from 'highlight.js/lib/languages/scala'

hljs.registerLanguage('cmake', cmake)
hljs.registerLanguage('gradle', gradle)
hljs.registerLanguage('groovy', groovy)
hljs.registerLanguage('powershell', powershell)
hljs.registerLanguage('protobuf', protobuf)
hljs.registerLanguage('scala', scala)

// Browsers can actually render these. HEIC/HEIF/TIFF deliberately excluded:
// Chrome does not decode them in <img>, a broken image is worse than download.
export const IMAGE = ['png', 'jpg', 'jpeg', 'jfif', 'gif', 'webp', 'svg', 'bmp', 'avif', 'apng', 'ico']
export const VIDEO = ['mp4', 'webm', 'ogv', 'mov', 'm4v']
export const AUDIO = ['mp3', 'wav', 'ogg', 'flac', 'm4a', 'aac', 'opus', 'weba']
export const PDF = ['pdf']
// Both extensions (txt, log, ...) and full names (go.sum, LICENSE, ...)
export const TEXT = [
  'txt', 'log', 'csv', 'tsv', 'env', 'gitignore', 'gitattributes', 'editorconfig',
  'properties', 'gradle', 'bat', 'cmd', 'text',
  'go.sum', 'go.work', 'cargo.lock', 'procfile', 'dockerignore',
  'license', 'licence', 'readme', 'changelog', 'copying', 'notice', 'authors', 'todo'
]

// Full filename first, then extension. Values are validated against hljs below.
const CODE_LANG = {
  go: 'go',
  ts: 'typescript',
  tsx: 'typescript', // no dedicated tsx grammar in hljs 11
  mts: 'typescript', // ESM TypeScript (.d.mts, .mts)
  cts: 'typescript', // CommonJS TypeScript
  js: 'javascript',
  jsx: 'javascript', // no dedicated jsx grammar in hljs 11
  mjs: 'javascript',
  cjs: 'javascript',
  py: 'python',
  rb: 'ruby',
  rs: 'rust',
  c: 'c',
  h: 'c',
  cc: 'cpp',
  cpp: 'cpp',
  cxx: 'cpp',
  hpp: 'cpp',
  cs: 'csharp',
  java: 'java',
  kt: 'kotlin',
  swift: 'swift',
  php: 'php',
  sh: 'bash',
  bash: 'bash',
  zsh: 'bash',
  yaml: 'yaml',
  yml: 'yaml',
  json: 'json',
  jsonc: 'json',
  jsonl: 'json', // NDJSON
  map: 'json', // source maps (.map) are JSON
  ipynb: 'json', // Jupyter notebooks are JSON
  toml: 'ini', // no toml grammar in hljs 11; ini covers [section]/key=value
  xml: 'xml',
  html: 'xml',
  htm: 'xml',
  css: 'css',
  scss: 'scss',
  less: 'less',
  sql: 'sql',
  md: 'markdown',
  dockerfile: 'dockerfile',
  makefile: 'makefile',
  ini: 'ini',
  conf: 'ini',
  cfg: 'ini',
  diff: 'diff',
  patch: 'diff',
  lua: 'lua',
  perl: 'perl',
  r: 'r',
  scala: 'scala',
  groovy: 'groovy',
  gradle: 'gradle',
  ps1: 'powershell',
  proto: 'protobuf',
  graphql: 'graphql',
  vue: 'xml',
  svelte: 'xml',
  // dotted / extension-less well-known names
  'go.mod': 'plaintext',
  'go.sum': 'plaintext',
  'cargo.lock': 'ini',
  '.gitmodules': 'ini',
  '.npmrc': 'ini',
  '.yarnrc': 'ini',
  '.bashrc': 'bash',
  '.bash_profile': 'bash',
  '.bash_aliases': 'bash',
  '.profile': 'bash',
  '.zshrc': 'bash',
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
export function codeLanguage(name) {
  const lower = name.toLowerCase()
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
  'application/javascript': 'javascript'
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
  'perl', 'r', 'scala', 'powershell', 'protobuf', 'cmake', 'graphql', 'plaintext'
]

const MIN_AUTO_DETECT = 64 // shorter samples carry too little signal

export function detectLanguage(text) {
  if (text.trim().length < MIN_AUTO_DETECT) return 'plaintext'
  const detected = hljs.highlightAuto(text.slice(0, 64 * 1024), AUTO_LANGS)
  return AUTO_LANGS.includes(detected.language) ? detected.language : 'plaintext'
}

export function viewerFor(name, mime) {
  const lower = name.toLowerCase()
  const ext = extension(lower)
  const mt = (mime || '').split(';')[0].trim().toLowerCase()

  if (mt.startsWith('image/')) return 'image'
  if (mt.startsWith('video/')) return 'video'
  if (mt.startsWith('audio/')) return 'audio'
  if (mt === 'application/pdf') return 'pdf'
  if (mt === 'application/json' || mt === 'application/xml') return 'code'
  if (mt.startsWith('text/')) return ext === 'svg' ? 'image' : 'code'
  // any other identified mime is binary non-media: the filename must not
  // override what the content says (a .json holding a zip is a zip)
  if (mt && mt !== 'application/octet-stream' && mt !== 'application/ogg') return 'download'
  // unidentified content: media extension is the only hint the sniffer lacks
  if (IMAGE.includes(ext)) return 'image'
  if (VIDEO.includes(ext)) return 'video'
  if (AUDIO.includes(ext)) return 'audio'
  if (PDF.includes(ext)) return 'pdf'
  if (codeLanguage(lower) !== null || TEXT.includes(ext) || TEXT.includes(lower)) return 'code'
  return 'download'
}
