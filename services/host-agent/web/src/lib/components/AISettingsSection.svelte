<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import EndpointCapability from '$lib/components/EndpointCapability.svelte';
	import InferenceCapability from '$lib/components/InferenceCapability.svelte';

	let chatBaseUrl = $state('');

	// Held as constants rather than inline attributes: each sentence is what the
	// form is for, and wrapping it inside a tag would bury it.
	const TRANSCRIPTION_DESCRIPTION =
		'An endpoint that turns audio into timed text. Apps that transcribe follow ' +
		'what is set here, and run without it if nothing is.';

	const EMBEDDINGS_DESCRIPTION =
		'An endpoint that turns text into vectors. An app that stores a memory index ' +
		'needs one. The model it first used fixes the index dimensions, so a later ' +
		'change does not match the vectors already stored.';

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
		Three capabilities, set here in one place: chat, transcription, and
		embeddings. Each is a separate endpoint because a server that answers one
		route is not known to answer the others, and the same box can do one and
		not the rest. Fill in what you run; an app that needs a capability you left
		empty says so rather than guessing. The API key for each goes to the secrets
		manager and is never read back by the browser.
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
	.ai-section {
		display: flex;
		flex-direction: column;
	}

	.section-description {
		margin: 0 0 var(--space-lg) 0;
		font-size: 0.875rem;
		color: var(--color-text-secondary);
		line-height: 1.5;
		max-width: 70ch;
	}
</style>
