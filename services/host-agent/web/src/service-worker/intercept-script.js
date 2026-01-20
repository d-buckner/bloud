// Storage Intercept Script
// This script is injected into iframe HTML by the service worker.
// It patches IndexedDB and localStorage to return configured values.
//
// Config is read from a meta tag: <meta name="bloud-intercept-config" content="...">
// Format: {
//   "indexedDB": { "dbName": { "storeName": { "key": "value" } } },
//   "localStorage": { "key": { "value": "exactValue" } | { "jsonPatch": { "path": "value" } } }
// }

(function () {
  'use strict';

  // DEBUG: Log auth state on every page load
  console.log('[bloud-intercept] Page loaded:', window.location.href);
  console.log('[bloud-intercept] document.cookie:', document.cookie || '(empty)');

  // Log all localStorage keys
  var lsKeys = [];
  for (var i = 0; i < localStorage.length; i++) {
    var key = localStorage.key(i);
    lsKeys.push(key);
    // Log auth-related keys with values
    if (key && (key.includes('auth') || key.includes('token') || key.includes('session') || key.includes('user'))) {
      console.log('[bloud-intercept] localStorage[' + key + ']:', localStorage.getItem(key));
    }
  }
  console.log('[bloud-intercept] localStorage keys:', lsKeys.join(', ') || '(empty)');

  // Log IndexedDB databases (async)
  if (indexedDB.databases) {
    indexedDB.databases().then(function(dbs) {
      console.log('[bloud-intercept] IndexedDB databases:', dbs.map(function(db) { return db.name; }).join(', ') || '(none)');
    }).catch(function() {});
  }

  var metaEl = document.querySelector('meta[name="bloud-intercept-config"]');
  if (!metaEl || !metaEl.content) {
    return;
  }

  var config;
  try {
    config = JSON.parse(metaEl.content);
  } catch (e) {
    console.error('[bloud-intercept] Failed to parse config:', e);
    return;
  }

  // IndexedDB interception
  if (config.indexedDB && Object.keys(config.indexedDB).length > 0) {
    setupIndexedDBIntercept(config.indexedDB);
  }

  // localStorage interception
  if (config.localStorage && Object.keys(config.localStorage).length > 0) {
    setupLocalStorageIntercept(config.localStorage);
  }

  function setupIndexedDBIntercept(intercepts) {
    var originalGet = IDBObjectStore.prototype.get;

    IDBObjectStore.prototype.get = function (key) {
      var dbName = this.transaction.db.name;
      var storeName = this.name;

      var dbIntercepts = intercepts[dbName];
      if (dbIntercepts) {
        var storeIntercepts = dbIntercepts[storeName];
        if (storeIntercepts && Object.prototype.hasOwnProperty.call(storeIntercepts, key)) {
          var value = storeIntercepts[key];
          console.log('[bloud-intercept] IndexedDB get:', dbName + '.' + storeName + '.' + key, '->', value);
          return createFakeIDBRequest(value, this);
        }
      }

      return originalGet.call(this, key);
    };
  }

  function createFakeIDBRequest(value, source) {
    var request = {
      result: value,
      error: null,
      source: source,
      transaction: source.transaction,
      readyState: 'done',
      onsuccess: null,
      onerror: null
    };

    Promise.resolve().then(function () {
      if (typeof request.onsuccess === 'function') {
        var event = { target: request, type: 'success' };
        request.onsuccess(event);
      }
    });

    return request;
  }

  function setupLocalStorageIntercept(intercepts) {
    var originalGetItem = Storage.prototype.getItem;

    Storage.prototype.getItem = function (key) {
      // Only intercept localStorage, not sessionStorage
      if (this !== window.localStorage) {
        return originalGetItem.call(this, key);
      }

      var interceptConfig = intercepts[key];
      if (!interceptConfig) {
        return originalGetItem.call(this, key);
      }

      // Simple value replacement
      if (interceptConfig.value !== undefined) {
        console.log('[bloud-intercept] localStorage get:', key, '-> (replaced)');
        return interceptConfig.value;
      }

      // JSON patch mode - get existing value and patch it
      if (interceptConfig.jsonPatch) {
        var stored = originalGetItem.call(this, key);
        if (!stored) {
          // No existing value, create object from patches
          var newObj = {};
          applyPatches(newObj, interceptConfig.jsonPatch);
          var result = JSON.stringify(newObj);
          console.log('[bloud-intercept] localStorage get:', key, '-> (created from patches)');
          return result;
        }

        try {
          var obj = JSON.parse(stored);
          applyPatches(obj, interceptConfig.jsonPatch);
          var patched = JSON.stringify(obj);
          console.log('[bloud-intercept] localStorage get:', key, '-> (patched)');
          return patched;
        } catch (e) {
          console.error('[bloud-intercept] Failed to patch JSON for key:', key, e);
          return stored;
        }
      }

      return originalGetItem.call(this, key);
    };
  }

  // Apply patches to an object using dot notation paths
  // Supports array indices: "Servers.0.ManualAddress"
  function applyPatches(obj, patches) {
    for (var path in patches) {
      if (Object.prototype.hasOwnProperty.call(patches, path)) {
        setByPath(obj, path, patches[path]);
      }
    }
  }

  function setByPath(obj, path, value) {
    var parts = path.split('.');
    var current = obj;

    for (var i = 0; i < parts.length - 1; i++) {
      var part = parts[i];
      var nextPart = parts[i + 1];
      var isNextIndex = /^\d+$/.test(nextPart);

      if (current[part] === undefined) {
        // Create array or object based on next part
        current[part] = isNextIndex ? [] : {};
      }

      current = current[part];
    }

    var lastPart = parts[parts.length - 1];
    current[lastPart] = value;
  }
})();
