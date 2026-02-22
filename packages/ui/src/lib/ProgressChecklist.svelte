<script lang="ts">
	export type ChecklistStatus = 'pending' | 'active' | 'done' | 'error';

	export interface ChecklistItem {
		label: string;
		status: ChecklistStatus;
	}

	interface Props {
		items: ChecklistItem[];
	}

	let { items }: Props = $props();

	const icons: Record<ChecklistStatus, string> = {
		pending: '○',
		active: '●',
		done: '✓',
		error: '✗'
	};
</script>

<ul class="checklist">
	{#each items as item}
		<li class="item" class:active={item.status === 'active'} class:done={item.status === 'done'} class:error={item.status === 'error'}>
			<span class="icon">{icons[item.status]}</span>
			<span class="label">{item.label}</span>
		</li>
	{/each}
</ul>

<style>
	.checklist {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.item {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		font-size: 0.9375rem;
		color: var(--color-text-muted);
	}

	.item.active {
		color: var(--color-text);
	}

	.item.done {
		color: var(--color-success);
	}

	.item.error {
		color: var(--color-error);
	}

	.icon {
		font-family: var(--font-mono);
		width: 1rem;
		text-align: center;
		flex-shrink: 0;
	}
</style>
