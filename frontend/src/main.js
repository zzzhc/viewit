import { mount } from 'svelte'
import './app.css'
import 'svelte-jsoneditor/themes/jse-theme-dark.css'
import App from './App.svelte'
import { initTheme } from './theme.svelte.js'

initTheme()

const app = mount(App, { target: document.getElementById('app') })

export default app
