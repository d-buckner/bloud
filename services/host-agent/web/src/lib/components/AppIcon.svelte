<script lang="ts">
	interface Props {
		appName: string;
		displayName?: string;
		size?: 'sm' | 'md' | 'lg';
		transparent?: boolean;
	}

	let { appName, displayName, size = 'md', transparent = false }: Props = $props();
	let imgFailed = $state(false);

	function getIconUrl(name: string): string {
		return `/api/apps/${name}/icon`;
	}

	function getLetter(name: string, display?: string): string {
		return (display || name).charAt(0).toUpperCase();
	}
</script>

<div class="app-icon" class:size-sm={size === 'sm'} class:size-lg={size === 'lg'} class:transparent>
	{#if !imgFailed}
		<img
			src={getIconUrl(appName)}
			alt=""
			onerror={() => imgFailed = true}
		/>
	{:else}
		<span class="app-letter">{getLetter(appName, displayName)}</span>
	{/if}
</div>

<style>
	.app-icon {
		width: 44px;
		height: 44px;
		display: flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
		position: relative;
		overflow: hidden;
	}

	.app-icon.transparent {
		background: transparent;
		border-color: transparent;
		box-shadow: none;
	}

	.app-icon.size-sm {
		width: 32px;
		height: 32px;
	}

	.app-icon.size-lg {
		width: 60px;
		height: 60px;
		border-radius: 14px;
	}

	.app-icon img {
		width: 28px;
		height: 28px;
		object-fit: contain;
	}

	.size-sm img {
		width: 20px;
		height: 20px;
	}

	.size-lg img {
		width: 44px;
		height: 44px;
	}

	.app-letter {
		font-size: 1.25rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}

	.size-sm .app-letter { font-size: 1rem; }
	.size-lg .app-letter { font-size: 1.75rem; }
</style>
