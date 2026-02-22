<script lang="ts">
	import { onMount, onDestroy } from 'svelte';

	interface LogEvent {
		phase: string;
		message: string;
	}

	interface Props {
		onRebootStarted: () => void;
		onFailed: () => void;
	}

	let { onRebootStarted, onFailed }: Props = $props();

	const friendlyMessages: Record<string, string> = {
		validating: 'Getting started…',
		partitioning: 'Preparing your drive…',
		formatting: 'Preparing your drive…',
		installing: 'Installing Bloud…',
		configuring: 'Almost there…',
		complete: 'All done.'
	};

	// Maps phase → which segment (0–2) is active
	// segment 0: disk prep, segment 1: install, segment 2: configure
	const phaseToSegment: Record<string, number> = {
		validating: 0,
		partitioning: 0,
		formatting: 0,
		installing: 1,
		configuring: 2,
		complete: 3 // past the last segment → all done
	};

	let currentPhase = $state('');
	let logLines = $state<string[]>([]);
	let failed = $state(false);
	let lastError = $state('');
	let showDetails = $state(false);
	let es: EventSource | null = null;

	let currentMessage = $derived(
		failed ? 'Something went wrong.' : (friendlyMessages[currentPhase] ?? 'Getting started…')
	);

	let activeSegment = $derived(phaseToSegment[currentPhase] ?? -1);
	let isComplete = $derived(currentPhase === 'complete');

	function segmentState(idx: number): 'done' | 'active' | 'pending' {
		if (failed) return 'pending';
		if (isComplete) return 'done';
		if (activeSegment === -1) return 'pending';
		if (idx < activeSegment) return 'done';
		if (idx === activeSegment) return 'active';
		return 'pending';
	}

	onMount(() => {
		es = new EventSource('/api/progress');

		es.onmessage = async (e) => {
			const event: LogEvent = JSON.parse(e.data);
			currentPhase = event.phase;

			if (event.message) {
				logLines = [...logLines, event.message];
			}

			if (event.phase === 'complete') {
				es?.close();
				try {
					await fetch('/api/reboot', { method: 'POST' });
				} catch {
					// reboot kills the connection — fetch error is expected
				}
				onRebootStarted();
			}

			if (event.phase === 'failed') {
				failed = true;
				lastError = event.message;
				showDetails = true;
				es?.close();
			}
		};

		es.onerror = () => {
			es?.close();
		};
	});

	onDestroy(() => {
		es?.close();
	});
</script>

<div class="card">
	<span class="wordmark">bloud</span>

	<div class="body">
		<h2 class="headline">
			{#if failed}
				Something went wrong.
			{:else}
				Setting up your server.
			{/if}
		</h2>

		{#if !failed}
			<div class="progress-track" role="progressbar" aria-label="Installation progress">
				{#each [0, 1, 2] as idx}
					<div
						class="segment"
						class:done={segmentState(idx) === 'done'}
						class:active={segmentState(idx) === 'active'}
					>
						<div class="segment-fill"></div>
					</div>
				{/each}
			</div>
		{/if}

		<div class="message-row">
			{#key currentMessage}
				<p class="message" class:message-error={failed}>{currentMessage}</p>
			{/key}
		</div>

		{#if failed}
			<p class="caption caption-error">Setup didn't complete.</p>
		{:else}
			<p class="caption">This takes a few minutes. Don't turn off your computer.</p>
		{/if}
	</div>

	<div class="details-area">
		<button
			class="details-toggle"
			type="button"
			onclick={() => (showDetails = !showDetails)}
			aria-expanded={showDetails}
		>
			<span class="toggle-arrow" class:open={showDetails}>▸</span>
			{showDetails ? 'Hide details' : 'Show details'}
		</button>

		{#if showDetails}
			<div class="log-box" role="log" aria-live="polite">
				{#each logLines as line}
					<div class="log-line">{line}</div>
				{/each}
				{#if logLines.length === 0}
					<span class="log-empty">Waiting for activity…</span>
				{/if}
			</div>
		{/if}
	</div>

	{#if failed}
		<div class="footer">
			<button class="retry-btn" onclick={onFailed}>Try Again</button>
		</div>
	{/if}
</div>

<style>
	.card {
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		padding: var(--space-2xl);
		width: 100%;
		max-width: 420px;
		box-shadow: var(--shadow-md);
		display: flex;
		flex-direction: column;
		gap: var(--space-xl);
	}

	.wordmark {
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		font-weight: 400;
		letter-spacing: 0.08em;
		color: var(--color-text-secondary);
		text-transform: lowercase;
	}

	/* Body */

	.body {
		display: flex;
		flex-direction: column;
		gap: var(--space-lg);
	}

	.headline {
		font-size: 1.375rem;
		font-weight: 400;
		color: var(--color-text);
		margin: 0;
		line-height: 1.3;
	}

	/* Progress bar */

	.progress-track {
		display: flex;
		gap: 6px;
	}

	.segment {
		flex: 1;
		height: 4px;
		background: var(--color-border);
		border-radius: 999px;
		position: relative;
		overflow: hidden;
	}

	.segment-fill {
		position: absolute;
		inset: 0;
		border-radius: 999px;
		background: var(--color-accent);
		width: 0%;
		transition: width 0.7s ease;
	}

	.segment.done .segment-fill {
		width: 100%;
	}

	.segment.active .segment-fill {
		animation: fill-creep 14s ease-out forwards;
	}

	.segment.active .segment-fill::after {
		content: '';
		position: absolute;
		top: 0;
		left: -60%;
		width: 60%;
		height: 100%;
		background: linear-gradient(90deg, transparent, rgba(255, 255, 255, 0.45), transparent);
		animation: shimmer 2.2s ease-in-out infinite;
	}

	@keyframes fill-creep {
		0% { width: 0%; }
		15% { width: 35%; }
		60% { width: 60%; }
		100% { width: 76%; }
	}

	@keyframes shimmer {
		0% { left: -60%; }
		100% { left: 160%; }
	}

	/* Message */

	.message-row {
		min-height: 1.5rem;
		position: relative;
	}

	.message {
		margin: 0;
		font-size: 0.9375rem;
		font-family: var(--font-serif);
		color: var(--color-text-secondary);
		animation: message-in 0.4s ease both;
	}

	.message-error {
		color: var(--color-error);
	}

	@keyframes message-in {
		from {
			opacity: 0;
			transform: translateY(4px);
		}
		to {
			opacity: 1;
			transform: translateY(0);
		}
	}

	.caption {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--color-text-muted);
		line-height: 1.5;
	}

	.caption-error {
		color: var(--color-error);
		opacity: 0.7;
	}

	/* Details */

	.details-area {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.details-toggle {
		background: none;
		border: none;
		padding: 0;
		cursor: pointer;
		display: inline-flex;
		align-items: center;
		gap: 5px;
		font-size: 0.8125rem;
		color: var(--color-text-muted);
		font-family: var(--font-serif);
		transition: color 0.15s ease;
	}

	.details-toggle:hover {
		color: var(--color-text-secondary);
	}

	.toggle-arrow {
		font-size: 0.5625rem;
		display: inline-block;
		transition: transform 0.2s ease;
		line-height: 1;
	}

	.toggle-arrow.open {
		transform: rotate(90deg);
	}

	.log-box {
		background: var(--color-bg-subtle);
		border: 1px solid var(--color-border-subtle);
		border-radius: var(--radius-md);
		padding: var(--space-sm) var(--space-md);
		max-height: 9rem;
		overflow-y: auto;
		display: flex;
		flex-direction: column;
		gap: 1px;
	}

	.log-line {
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		color: var(--color-text-secondary);
		line-height: 1.6;
		word-break: break-all;
	}

	.log-empty {
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		font-style: italic;
	}

	/* Failed footer */

	.footer {
		display: flex;
	}

	.retry-btn {
		background: none;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text);
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		padding: var(--space-sm) var(--space-lg);
		cursor: pointer;
		transition: background 0.15s ease, border-color 0.15s ease;
	}

	.retry-btn:hover {
		background: var(--color-bg-subtle);
		border-color: var(--color-accent);
	}
</style>
