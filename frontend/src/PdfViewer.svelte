<script>
  import { fileUrl } from './api.js'

  let { path } = $props()
</script>

<div class="viewer pdf-viewer">
  <iframe src={fileUrl(path)} title="pdf 预览"></iframe>
</div>
