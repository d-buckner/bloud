<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import EndpointCapability from '$lib/components/EndpointCapability.svelte';
	import InferenceCapability from '$lib/components/InferenceCapability.svelte';

	// Held as constants rather than inline attributes: each one names what the
	// form is for, and wrapping it inside a tag would bury it.
	const TRANSCRIPTION_DESCRIPTION = 'Speech to text';

	const EMBEDDINGS_DESCRIPTION =
		'Text to vectors. Changing the model later will not match stored vectors.';
</script>

<section class="section ai-section">
	<h2>AI</h2>
	<p class="section-description">
		Chat, transcription, and embedding settings
	</p>

	<div class="ai-capabilities">
		<InferenceCapability />

		<EndpointCapability
			contract="transcription"
			label="Transcription"
			defaultName="Transcription"
			placeholder="https://api.openai.com/v1"
			description={TRANSCRIPTION_DESCRIPTION}
		/>

		<EndpointCapability
			contract="embeddings"
			label="Embeddings"
			defaultName="Embeddings"
			placeholder="https://api.openai.com/v1"
			description={EMBEDDINGS_DESCRIPTION}
		/>
	</div>
</section>

<style>
	/* The settings grid owns the spacing between sections, so this one carries no
	   rule of its own. */
	.ai-section {
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
	}

	/* The three capability forms flow into columns once there is room. Stacked,
	   they left the right half of a full-width section empty and made the section
	   the tallest thing on the page. */
	.ai-capabilities {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 320px), 1fr));
		gap: var(--space-lg);
		align-items: start;
	}

	/* No max-width of its own: the page container and the grid own the width. */
	.section-description {
		margin: 0;
		font-size: 0.875rem;
		color: var(--color-text-secondary);
		line-height: 1.5;
	}
</style>
