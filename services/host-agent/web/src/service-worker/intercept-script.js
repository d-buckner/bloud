// IndexedDB Get Intercept Script
// This script is injected into iframe HTML by the service worker.
// It patches IDBObjectStore.prototype.get to return configured values
// regardless of what's actually stored in the database.
//
// Config is read from a meta tag: <meta name="bloud-idb-config" content="...">
// Format: { "dbName": { "storeName": { "key": "value" } } }

(function () {
  'use strict';

  var metaEl = document.querySelector('meta[name="bloud-idb-config"]');
  if (!metaEl || !metaEl.content) {
    return;
  }

  var intercepts;
  try {
    intercepts = JSON.parse(metaEl.content);
  } catch (e) {
    console.error('[bloud-idb] Failed to parse intercept config:', e);
    return;
  }

  var originalGet = IDBObjectStore.prototype.get;

  IDBObjectStore.prototype.get = function (key) {
    var dbName = this.transaction.db.name;
    var storeName = this.name;

    // Check if this key should be intercepted
    var dbIntercepts = intercepts[dbName];
    if (dbIntercepts) {
      var storeIntercepts = dbIntercepts[storeName];
      if (storeIntercepts && Object.prototype.hasOwnProperty.call(storeIntercepts, key)) {
        var value = storeIntercepts[key];
        console.log('[bloud-idb] Intercepting get:', dbName + '.' + storeName + '.' + key, '->', value);
        return createFakeRequest(value, this);
      }
    }

    return originalGet.call(this, key);
  };

  function createFakeRequest(value, source) {
    // Create an object that mimics IDBRequest
    var request = {
      result: value,
      error: null,
      source: source,
      transaction: source.transaction,
      readyState: 'done',
      onsuccess: null,
      onerror: null
    };

    // Simulate async behavior - callbacks fire after current microtask
    Promise.resolve().then(function () {
      if (typeof request.onsuccess === 'function') {
        var event = { target: request, type: 'success' };
        request.onsuccess(event);
      }
    });

    return request;
  }
})();
