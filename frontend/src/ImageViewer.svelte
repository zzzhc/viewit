<script>
  import { onMount } from 'svelte'
  import { fileUrl } from './api.js'
  import Viewer from 'viewerjs'
  import 'viewerjs/dist/viewer.css'

  let { path } = $props()

  let source

  onMount(() => {
    // viewerjs inserts its container as a sibling of `source` (inside
    // .image-viewer), so it stays attached to our root even when Svelte
    // detaches the component — destroy() then never sees a null parentNode.
    const viewer = new Viewer(source, {
      inline: true,
      button: false,
      navbar: false,
      title: false,
      toolbar: {
        zoomIn: true,
        zoomOut: true,
        oneToOne: true,
        reset: true,
        prev: false,
        play: false,
        next: false,
        rotateLeft: true,
        rotateRight: true,
        flipHorizontal: true,
        flipVertical: true
      },
      tooltip: true,
      movable: true,
      zoomable: true,
      rotatable: true,
      scalable: true,
      transition: false
    })
    return () => viewer.destroy()
  })
</script>

<div class="viewer image-viewer">
  <div class="image-viewer-source" bind:this={source}>
    <img src={fileUrl(path)} alt="" />
  </div>
</div>
