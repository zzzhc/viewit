<script>
  import { fileUrl } from './api.js'

  let { path } = $props()
</script>

<div class="viewer media-viewer">
  <video controls src={fileUrl(path)}></video>
</div>
