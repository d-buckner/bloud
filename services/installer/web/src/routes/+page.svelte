<script lang="ts">
	import '../app.css';
	import Welcome from '$lib/steps/Welcome.svelte';
	import Installing from '$lib/steps/Installing.svelte';
	import Restarting from '$lib/steps/Restarting.svelte';

	type Step = 'welcome' | 'installing' | 'restarting';

	let step = $state<Step>('welcome');
</script>

<main>
	{#if step === 'welcome'}
		<Welcome
			onInstallStarted={() => {
				step = 'installing';
			}}
		/>
	{:else if step === 'installing'}
		<Installing
			onRebootStarted={() => {
				step = 'restarting';
			}}
			onFailed={() => {
				step = 'welcome';
			}}
		/>
	{:else if step === 'restarting'}
		<Restarting />
	{/if}
</main>

<style>
	main {
		min-height: 100vh;
		display: flex;
		align-items: center;
		justify-content: center;
		padding: var(--space-xl);
	}
</style>
