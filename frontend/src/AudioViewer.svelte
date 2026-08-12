<script>
  import { fileUrl } from './api.js'

  let { path } = $props()
</script>

<div class="viewer media-viewer">
  <audio controls src={fileUrl(path)}></audio>
</div>
