// IndexedDB Intercept Injection
// Builds HTML to inject into iframe responses for intercepting IndexedDB reads

import interceptScript from './intercept-script.js?raw';

/**
 * Configuration for IndexedDB intercepts sent from main page
 */
export interface IndexedDBInterceptConfig {
  database: string;
  intercepts: Array<{
    store: string;
    key: string;
    value: string;
  }>;
}

/**
 * Intercept map structure used by the injected script
 * Format: { dbName: { storeName: { key: value } } }
 */
export type InterceptMap = Record<string, Record<string, Record<string, string>>>;

/**
 * Build the intercept map from config
 * Exported for testing
 */
export function buildInterceptMap(config: IndexedDBInterceptConfig): InterceptMap {
  const map: InterceptMap = {
    [config.database]: {},
  };

  for (const entry of config.intercepts) {
    if (!map[config.database][entry.store]) {
      map[config.database][entry.store] = {};
    }
    map[config.database][entry.store][entry.key] = entry.value;
  }

  return map;
}

/**
 * Escape string for safe embedding in HTML attribute.
 * Encodes characters that have special meaning in HTML attributes.
 */
export function escapeForAttribute(str: string): string {
  return str
    .replace(/&/g, '&amp;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

/**
 * Generate the HTML to inject into iframe <head>
 * Returns empty string if no intercepts configured
 */
export function generateInterceptHtml(config: IndexedDBInterceptConfig | null): string {
  if (!config || config.intercepts.length === 0) {
    return '';
  }

  const interceptMap = buildInterceptMap(config);
  const configJson = escapeForAttribute(JSON.stringify(interceptMap));

  // Meta tag for config + script that reads it
  return `<meta name="bloud-idb-config" content="${configJson}">
<script>${interceptScript}</script>`;
}

/**
 * Inject intercept script into HTML response
 * Returns original HTML if injection point not found
 */
export function injectIntoHtml(html: string, config: IndexedDBInterceptConfig | null): string {
  const injection = generateInterceptHtml(config);
  if (!injection) {
    return html;
  }

  // Try to inject after <head> tag (case insensitive)
  const headMatch = html.match(/<head([^>]*)>/i);
  if (headMatch) {
    const insertPos = headMatch.index! + headMatch[0].length;
    return html.slice(0, insertPos) + injection + html.slice(insertPos);
  }

  // Fallback: inject after <!DOCTYPE html> or at the start
  const doctypeMatch = html.match(/<!DOCTYPE[^>]*>/i);
  if (doctypeMatch) {
    const insertPos = doctypeMatch.index! + doctypeMatch[0].length;
    return html.slice(0, insertPos) + injection + html.slice(insertPos);
  }

  // Last resort: prepend
  return injection + html;
}
