// IndexedDB decorator injection script generator
// Creates a script that intercepts IDBObjectStore.get for protected keys

import type { ProtectedEntry } from './types';

/**
 * Generate the injection script for protected IndexedDB entries.
 * Returns a self-executing function that decorates IDBObjectStore.prototype.get.
 */
export function generateInjectionScript(entries: ProtectedEntry[]): string {
  if (entries.length === 0) {
    return '';
  }

  // Build the protected values map: "database:store:key" -> value
  const protectedMap: Record<string, string> = {};
  for (const entry of entries) {
    const key = `${entry.database}:${entry.store}:${entry.key}`;
    protectedMap[key] = entry.value;
  }

  // The injection script - runs before app code
  return `<script>
(function() {
  var PROTECTED = ${JSON.stringify(protectedMap)};

  var originalGet = IDBObjectStore.prototype.get;
  IDBObjectStore.prototype.get = function(key) {
    var db = this.transaction.db.name;
    var lookupKey = db + ':' + this.name + ':' + key;

    if (lookupKey in PROTECTED) {
      return createFakeIDBRequest(PROTECTED[lookupKey]);
    }
    return originalGet.call(this, key);
  };

  function createFakeIDBRequest(value) {
    var onsuccessHandler = null;
    var onerrorHandler = null;
    var listeners = { success: [], error: [] };

    var fakeRequest = {
      result: value,
      error: null,
      source: null,
      transaction: null,
      readyState: 'done',

      get onsuccess() { return onsuccessHandler; },
      set onsuccess(handler) {
        onsuccessHandler = handler;
        if (handler) {
          setTimeout(function() {
            var event = new Event('success');
            Object.defineProperty(event, 'target', { value: fakeRequest });
            handler.call(fakeRequest, event);
          }, 0);
        }
      },

      get onerror() { return onerrorHandler; },
      set onerror(handler) { onerrorHandler = handler; },

      addEventListener: function(type, handler) {
        if (type === 'success') {
          setTimeout(function() {
            var event = new Event('success');
            Object.defineProperty(event, 'target', { value: fakeRequest });
            handler.call(fakeRequest, event);
          }, 0);
        }
        if (listeners[type]) {
          listeners[type].push(handler);
        }
      },

      removeEventListener: function(type, handler) {
        var arr = listeners[type];
        if (arr) {
          var idx = arr.indexOf(handler);
          if (idx >= 0) arr.splice(idx, 1);
        }
      },

      dispatchEvent: function() { return true; }
    };

    return fakeRequest;
  }

  console.log('[bloud] IndexedDB protection active for:', Object.keys(PROTECTED));
})();
</script>`;
}

/**
 * Inject the protection script into an HTML response body.
 * Inserts the script at the start of <head> to run before app code.
 */
export function injectIntoHtml(html: string, entries: ProtectedEntry[]): string {
  const script = generateInjectionScript(entries);
  if (!script) {
    return html;
  }

  // Try to inject after <head> tag
  const headMatch = html.match(/<head[^>]*>/i);
  if (headMatch) {
    const insertPos = headMatch.index! + headMatch[0].length;
    return html.slice(0, insertPos) + script + html.slice(insertPos);
  }

  // Fallback: inject after <!DOCTYPE> or at start
  const doctypeMatch = html.match(/<!DOCTYPE[^>]*>/i);
  if (doctypeMatch) {
    const insertPos = doctypeMatch.index! + doctypeMatch[0].length;
    return html.slice(0, insertPos) + script + html.slice(insertPos);
  }

  // Last resort: prepend
  return script + html;
}
