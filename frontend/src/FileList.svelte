<script>
  import { formatSize, formatDate } from './format.js'

  let { entries, onNavigate } = $props()
</script>

{#if entries.length === 0}
  <div class="hint">此目录为空</div>
{:else}
  <table class="file-list">
    <thead>
      <tr>
        <th>名称</th>
        <th class="size-col">大小</th>
        <th class="date-col">修改时间</th>
      </tr>
    </thead>
    <tbody>
      {#each entries as entry (entry.name)}
        <tr onclick={() => onNavigate(entry.name)}>
          <td>
            <span class="name">
              {#if entry.isDir}
                <svg class="icon" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" /></svg>
              {:else}
                <svg class="icon" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" /><polyline points="14 2 14 8 20 8" /></svg>
              {/if}
              {entry.name}
            </span>
          </td>
          <td class="size-col">{entry.isDir ? '-' : formatSize(entry.size)}</td>
          <td class="date-col">{formatDate(entry.modTime)}</td>
        </tr>
      {/each}
    </tbody>
  </table>
{/if}
