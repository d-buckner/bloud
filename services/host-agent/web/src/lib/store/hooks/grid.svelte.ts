// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Grid actions, scoped to the grid and the widget picker.
 *
 * A tile drag and a widget toggle are writes, so they come from the platform and
 * not from the command module. A component that draws the grid gets these three
 * and nothing else.
 */

import type { GridElement } from '$lib/types';
import { commands } from '../commands';
import type { CommandResult } from '../runner';

export interface GridActions {
	saveLayout(elements?: GridElement[]): Promise<CommandResult<null>>;
	toggleWidget(widgetId: string): void;
	removeWidget(widgetId: string): void;
}

const ACTIONS: GridActions = {
	saveLayout: (elements) => commands.saveLayout(elements),
	toggleWidget: (widgetId) => commands.toggleWidget(widgetId),
	removeWidget: (widgetId) => commands.removeWidget(widgetId)
};

export function useGridActions(): GridActions {
	return ACTIONS;
}
