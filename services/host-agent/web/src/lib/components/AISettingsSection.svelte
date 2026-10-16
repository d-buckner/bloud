<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import EndpointCapability from '$lib/components/EndpointCapability.svelte';
	import InferenceCapability from '$lib/components/InferenceCapability.svelte';

	let chatBaseUrl = $state('');

	// Held as constants rather than inline attributes: each one names what the form
	// is for, and wrapping it inside a tag would bury it.
	const TRANSCRIPTION_DESCRIPTION = 'Speech to text';

	const EMBEDDINGS_DESCRIPTION =
		'Text to vectors. Changing the model later will not match stored vectors.';

	// The chat form owns its own state. It reports the one fact the other two
	// forms use: the address worth offering as a one-click fill, because one
	// gateway serving every capability is the common case and a server that
	// answers chat is not known to answer either other route.
	function onbaseUrl(url: string) {
		chatBaseUrl = url;
	}
</script>

<section class="section ai-section">
	<h2>AI</h2>
	<p class="section-description">
		Chat, transcription, and embedding settings
	</p>

	<InferenceCapability {onbaseUrl} />

	<EndpointCapability
		contract="transcription"
		label="Transcription"
		defaultName="Transcription"
		placeholder="https://api.openai.com/v1"
		aiBaseUrl={chatBaseUrl}
		description={TRANSCRIPTION_DESCRIPTION}
	/>

	<EndpointCapability
		contract="embeddings"
		label="Embeddings"
		defaultName="Embeddings"
		placeholder="https://api.openai.com/v1"
		aiBaseUrl={chatBaseUrl}
		description={EMBEDDINGS_DESCRIPTION}
	/>
</section>

<style>
	/* The same separator the Users section carries. This section follows another,
	   so it needs a break above it; it had none, which is what let the AI heading
	   read as the tail of the section above. The gap spaces the three capability
	   blocks, which carry no separator of their own, so none needs a top margin. */
	.ai-section {
		display: flex;
		flex-direction: column;
		gap: var(--space-lg);
		margin-top: var(--space-2xl);
		padding-top: var(--space-2xl);
		border-top: 1px solid var(--color-border);
	}

	/* No max-width of its own: the shared `.section` measure already caps it. */
	.section-description {
		margin: 0;
		font-size: 0.875rem;
		color: var(--color-text-secondary);
		line-height: 1.5;
	}
</style>
