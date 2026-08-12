<script>
  import { formatSize, formatDate } from './format.js'
  import { downloadUrl } from './api.js'

  let { entries, path, onNavigate } = $props()

  function entryPath(name) {
    return path === '/' ? name : path + '/' + name
  }
</script>

{#if entries.length === 0}
  <div class="hint">此目录为空</div>
{:else}
  <table class="w-full border-collapse">
    <thead>
      <tr>
        <th class="sticky top-0 whitespace-nowrap bg-panel px-3 py-[7px] text-left text-xs font-semibold uppercase tracking-wider text-muted">名称</th>
        <th class="sticky top-0 w-[1%] whitespace-nowrap bg-panel px-3 py-[7px] text-left text-xs font-semibold uppercase tracking-wider text-muted">大小</th>
        <th class="sticky top-0 w-[1%] whitespace-nowrap bg-panel px-3 py-[7px] text-left text-xs font-semibold uppercase tracking-wider text-muted">修改时间</th>
        <th class="sticky top-0 w-[1%] whitespace-nowrap bg-panel px-3 py-[7px] text-right text-xs font-semibold uppercase tracking-wider text-muted">操作</th>
      </tr>
    </thead>
    <tbody>
      {#each entries as entry (entry.name)}
        <tr class="cursor-pointer whitespace-nowrap hover:bg-hover" onclick={() => onNavigate(entry.name)}>
          <td class="border-b border-edge px-3 py-[7px]">
            <span class="inline-flex items-center gap-2">
              {#if entry.isDir}
                <svg class="flex-none text-muted" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" /></svg>
              {:else}
                <svg class="flex-none text-muted" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" /><polyline points="14 2 14 8 20 8" /></svg>
              {/if}
              {entry.name}
            </span>
          </td>
          <td class="border-b border-edge px-3 py-[7px] text-muted">{entry.isDir ? '-' : formatSize(entry.size)}</td>
          <td class="border-b border-edge px-3 py-[7px] text-muted">{formatDate(entry.modTime)}</td>
          <td class="border-b border-edge px-3 py-[7px] text-right">
            <a class="inline-flex h-7 w-7 items-center justify-center rounded text-muted no-underline hover:bg-hover hover:text-fg" title="下载" href={downloadUrl(entryPath(entry.name))}
               onclick={(e) => e.stopPropagation()}>
              <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" /><polyline points="7 10 12 15 17 10" /><line x1="12" y1="15" x2="12" y2="3" /></svg>
            </a>
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
{/if}
