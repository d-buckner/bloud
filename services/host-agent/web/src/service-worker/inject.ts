// Storage Intercept Injection
// Builds HTML to inject into iframe responses for intercepting IndexedDB and localStorage reads

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
 * Configuration for localStorage intercepts sent from main page
 */
export interface LocalStorageInterceptConfig {
  intercepts: Array<{
    key: string;
    value?: string;
    jsonPatch?: Record<string, string>;
  }>;
}

/**
 * Unified intercept configuration
 */
export interface InterceptConfig {
  indexedDB?: IndexedDBInterceptConfig | null;
  localStorage?: LocalStorageInterceptConfig | null;
}

/**
 * IndexedDB map structure used by the injected script
 * Format: { dbName: { storeName: { key: value } } }
 */
export type IndexedDBInterceptMap = Record<string, Record<string, Record<string, string>>>;

/**
 * localStorage map structure used by the injected script
 * Format: { key: { value: "..." } | { jsonPatch: { path: value } } }
 */
export type LocalStorageInterceptMap = Record<string, { value?: string; jsonPatch?: Record<string, string> }>;

/**
 * Full config structure passed to the injected script
 */
export interface ScriptInterceptConfig {
  indexedDB?: IndexedDBInterceptMap;
  localStorage?: LocalStorageInterceptMap;
}

/**
 * Build the IndexedDB intercept map from config
 * Exported for testing
 */
export function buildIndexedDBInterceptMap(config: IndexedDBInterceptConfig): IndexedDBInterceptMap {
  const map: IndexedDBInterceptMap = {
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
 * Build the localStorage intercept map from config
 * Exported for testing
 */
export function buildLocalStorageInterceptMap(config: LocalStorageInterceptConfig): LocalStorageInterceptMap {
  const map: LocalStorageInterceptMap = {};

  for (const entry of config.intercepts) {
    if (entry.value !== undefined) {
      map[entry.key] = { value: entry.value };
    } else if (entry.jsonPatch) {
      map[entry.key] = { jsonPatch: entry.jsonPatch };
    }
  }

  return map;
}

/**
 * Build the unified script config from intercept config
 * Exported for testing
 */
export function buildScriptConfig(config: InterceptConfig): ScriptInterceptConfig | null {
  const scriptConfig: ScriptInterceptConfig = {};

  if (config.indexedDB && config.indexedDB.intercepts.length > 0) {
    scriptConfig.indexedDB = buildIndexedDBInterceptMap(config.indexedDB);
  }

  if (config.localStorage && config.localStorage.intercepts.length > 0) {
    scriptConfig.localStorage = buildLocalStorageInterceptMap(config.localStorage);
  }

  // Return null if no intercepts configured
  if (!scriptConfig.indexedDB && !scriptConfig.localStorage) {
    return null;
  }

  return scriptConfig;
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
export function generateInterceptHtml(config: InterceptConfig | null): string {
  if (!config) {
    return '';
  }

  const scriptConfig = buildScriptConfig(config);
  if (!scriptConfig) {
    return '';
  }

  const configJson = escapeForAttribute(JSON.stringify(scriptConfig));

  // Meta tag for config + script that reads it
  return `<meta name="bloud-intercept-config" content="${configJson}">
<script>${interceptScript}</script>`;
}

/**
 * Inject intercept script into HTML response
 * Returns original HTML if injection point not found
 */
export function injectIntoHtml(html: string, config: InterceptConfig | null): string {
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
