<script lang="ts">
	import Widget from './Widget.svelte';
	import WidgetPicker from './WidgetPicker.svelte';
	import WeatherConfig from './WeatherConfig.svelte';
	import { enabledWidgets, widgetPrefs, getWidgetConfig } from '$lib/stores/widgetPrefs';
	import { defaultWeatherConfig, type WeatherConfig as WeatherConfigType } from './weather';

	let showPicker = $state(false);
	let configuringWidget = $state<string | null>(null);

	// Drag-to-reorder state
	let draggedId = $state<string | null>(null);
	let dragOverId = $state<string | null>(null);

	function handleRemove(widgetId: string): void {
		widgetPrefs.disableWidget(widgetId);
	}

	function handleConfigure(widgetId: string): void {
		configuringWidget = widgetId;
	}

	function handleSaveWeatherConfig(config: WeatherConfigType): void {
		widgetPrefs.setWidgetConfig('weather', config as unknown as Record<string, unknown>);
		configuringWidget = null;
	}

	function getWeatherConfig(): WeatherConfigType {
		const config = getWidgetConfig('weather');
		if (
			typeof config.latitude === 'number' &&
			typeof config.longitude === 'number' &&
			typeof config.locationName === 'string'
		) {
			return {
				latitude: config.latitude,
				longitude: config.longitude,
				locationName: config.locationName,
			};
		}
		return defaultWeatherConfig;
	}

	// Drag-to-reorder handlers
	function handleDragStart(e: DragEvent, widgetId: string): void {
		draggedId = widgetId;
		if (e.dataTransfer) {
			e.dataTransfer.effectAllowed = 'move';
			e.dataTransfer.setData('text/plain', widgetId);
		}
	}

	function handleDragOver(e: DragEvent, widgetId: string): void {
		e.preventDefault();
		if (draggedId && draggedId !== widgetId) {
			dragOverId = widgetId;
			if (e.dataTransfer) {
				e.dataTransfer.dropEffect = 'move';
			}
		}
	}

	function handleDragLeave(): void {
		dragOverId = null;
	}

	function handleDrop(e: DragEvent, targetId: string): void {
		e.preventDefault();
		if (!draggedId || draggedId === targetId) {
			draggedId = null;
			dragOverId = null;
			return;
		}

		// Get current order
		const currentOrder = $enabledWidgets.map((w) => w.id);
		const draggedIndex = currentOrder.indexOf(draggedId);
		const targetIndex = currentOrder.indexOf(targetId);

		if (draggedIndex === -1 || targetIndex === -1) {
			draggedId = null;
			dragOverId = null;
			return;
		}

		// Remove dragged item and insert at target position
		const newOrder = [...currentOrder];
		newOrder.splice(draggedIndex, 1);
		newOrder.splice(targetIndex, 0, draggedId);

		widgetPrefs.reorder(newOrder);
		draggedId = null;
		dragOverId = null;
	}

	function handleDragEnd(): void {
		draggedId = null;
		dragOverId = null;
	}
</script>

<div class="widget-container">
	{#if $enabledWidgets.length === 0}
		<button class="widgets-placeholder" onclick={() => showPicker = true}>
			<span class="widgets-placeholder-text">Add widgets to your dashboard</span>
			<span class="widgets-placeholder-icon">+</span>
		</button>
	{:else}
		<div class="widget-grid">
			{#each $enabledWidgets as widget (widget.id)}
				<div
					class="widget-slot"
					class:dragging={draggedId === widget.id}
					class:drag-over={dragOverId === widget.id}
					style="grid-column: span {widget.size.cols}; grid-row: span {widget.size.rows};"
					draggable="true"
					ondragstart={(e) => handleDragStart(e, widget.id)}
					ondragover={(e) => handleDragOver(e, widget.id)}
					ondragleave={handleDragLeave}
					ondrop={(e) => handleDrop(e, widget.id)}
					ondragend={handleDragEnd}
					role="listitem"
				>
					<Widget title={widget.name} onRemove={() => handleRemove(widget.id)}>
						<widget.component
							config={getWidgetConfig(widget.id)}
							onConfigure={widget.configurable ? () => handleConfigure(widget.id) : undefined}
						/>
					</Widget>
				</div>
			{/each}
			<button class="add-widget-btn" onclick={() => showPicker = true} aria-label="Add widget">
				<span class="add-icon">+</span>
			</button>
		</div>
	{/if}
</div>

<WidgetPicker open={showPicker} onclose={() => showPicker = false} />

<WeatherConfig
	open={configuringWidget === 'weather'}
	onclose={() => configuringWidget = null}
	currentConfig={getWeatherConfig()}
	onSave={handleSaveWeatherConfig}
/>

<style>
	.widget-container {
		width: 100%;
		max-width: 600px;
		container-type: inline-size;
	}

	.widgets-placeholder {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: var(--space-sm);
		width: 100%;
		padding: var(--space-xl);
		background: var(--color-bg-elevated);
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-lg);
		cursor: pointer;
		transition: border-color 0.15s ease, background 0.15s ease;
	}

	.widgets-placeholder:hover {
		border-color: var(--color-text-muted);
		background: var(--color-bg-subtle);
	}

	.widgets-placeholder-text {
		color: var(--color-text-muted);
		font-size: 0.875rem;
	}

	.widgets-placeholder-icon {
		width: 32px;
		height: 32px;
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 1.5rem;
		color: var(--color-text-muted);
		background: var(--color-bg);
		border-radius: 50%;
	}

	.widget-grid {
		display: grid;
		grid-template-columns: repeat(2, 1fr);
		grid-auto-rows: calc((100cqi - var(--space-lg)) / 2);
		gap: var(--space-lg);
	}

	.widget-slot {
		display: flex;
		flex-direction: column;
	}

	.widget-slot :global(.widget) {
		flex: 1;
		display: flex;
		flex-direction: column;
	}

	.widget-slot :global(.widget-content) {
		flex: 1;
	}

	.widget-slot.dragging {
		opacity: 0.5;
		cursor: grabbing;
	}

	.widget-slot.drag-over {
		outline: 2px dashed var(--color-accent);
		outline-offset: 2px;
	}

	.widget-slot:not(.dragging) {
		cursor: grab;
	}

	.add-widget-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		min-height: 80px;
		background: var(--color-bg-elevated);
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-lg);
		cursor: pointer;
		transition: border-color 0.15s ease, background 0.15s ease;
	}

	.add-widget-btn:hover {
		border-color: var(--color-text-muted);
		background: var(--color-bg-subtle);
	}

	.add-icon {
		width: 32px;
		height: 32px;
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 1.5rem;
		color: var(--color-text-muted);
	}

	@media (max-width: 500px) {
		.widget-grid {
			grid-template-columns: 1fr;
			grid-auto-rows: 100cqi;
		}
	}
</style>
