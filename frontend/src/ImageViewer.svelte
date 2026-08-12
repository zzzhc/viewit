<script>
  import { fileUrl } from './api.js'

  let { path } = $props()
  let natural = $state(false)
</script>

<div
  class="viewer image-viewer"
  class:natural
  onclick={() => (natural = !natural)}
  title={natural ? '点击恢复适应窗口' : '点击查看原始尺寸'}
>
  <img src={fileUrl(path)} alt="" />
</div>
