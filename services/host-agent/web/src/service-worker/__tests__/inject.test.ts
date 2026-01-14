import { describe, it, expect } from 'vitest';
import {
  buildInterceptMap,
  generateInterceptHtml,
  injectIntoHtml,
  escapeForAttribute,
  type IndexedDBInterceptConfig,
} from '../inject';

describe('inject', () => {
  describe('buildInterceptMap', () => {
    it('builds map with single entry', () => {
      const config: IndexedDBInterceptConfig = {
        database: 'mydb',
        intercepts: [{ store: 'mystore', key: 'mykey', value: 'myvalue' }],
      };

      const result = buildInterceptMap(config);

      expect(result).toEqual({
        mydb: {
          mystore: {
            mykey: 'myvalue',
          },
        },
      });
    });

    it('builds map with multiple entries in same store', () => {
      const config: IndexedDBInterceptConfig = {
        database: 'actual',
        intercepts: [
          { store: 'asyncStorage', key: 'server-url', value: 'http://example.com' },
          { store: 'asyncStorage', key: 'theme', value: 'dark' },
        ],
      };

      const result = buildInterceptMap(config);

      expect(result).toEqual({
        actual: {
          asyncStorage: {
            'server-url': 'http://example.com',
            theme: 'dark',
          },
        },
      });
    });

    it('builds map with entries in different stores', () => {
      const config: IndexedDBInterceptConfig = {
        database: 'testdb',
        intercepts: [
          { store: 'store1', key: 'key1', value: 'value1' },
          { store: 'store2', key: 'key2', value: 'value2' },
        ],
      };

      const result = buildInterceptMap(config);

      expect(result).toEqual({
        testdb: {
          store1: { key1: 'value1' },
          store2: { key2: 'value2' },
        },
      });
    });

    it('handles empty intercepts array', () => {
      const config: IndexedDBInterceptConfig = {
        database: 'emptydb',
        intercepts: [],
      };

      const result = buildInterceptMap(config);

      expect(result).toEqual({ emptydb: {} });
    });

    it('handles special characters in values', () => {
      const config: IndexedDBInterceptConfig = {
        database: 'db',
        intercepts: [
          { store: 'store', key: 'url', value: 'http://localhost:8080/embed/app/' },
          { store: 'store', key: 'json', value: '{"nested": "value"}' },
        ],
      };

      const result = buildInterceptMap(config);

      expect(result.db.store.url).toBe('http://localhost:8080/embed/app/');
      expect(result.db.store.json).toBe('{"nested": "value"}');
    });
  });

  describe('escapeForAttribute', () => {
    it('escapes double quotes', () => {
      expect(escapeForAttribute('"hello"')).toBe('&quot;hello&quot;');
    });

    it('escapes ampersands', () => {
      expect(escapeForAttribute('a&b')).toBe('a&amp;b');
    });

    it('escapes angle brackets', () => {
      expect(escapeForAttribute('<script>')).toBe('&lt;script&gt;');
    });

    it('escapes single quotes', () => {
      expect(escapeForAttribute("it's")).toBe('it&#39;s');
    });

    it('handles JSON with special characters', () => {
      const json = '{"key":"<script>alert(\\"xss\\")</script>"}';
      const escaped = escapeForAttribute(json);
      expect(escaped).not.toContain('<script>');
      expect(escaped).toContain('&lt;script&gt;');
    });
  });

  describe('generateInterceptHtml', () => {
    it('returns empty string for null config', () => {
      const result = generateInterceptHtml(null);
      expect(result).toBe('');
    });

    it('returns empty string for empty intercepts', () => {
      const config: IndexedDBInterceptConfig = {
        database: 'db',
        intercepts: [],
      };

      const result = generateInterceptHtml(config);
      expect(result).toBe('');
    });

    it('generates meta tag with config', () => {
      const config: IndexedDBInterceptConfig = {
        database: 'actual',
        intercepts: [{ store: 'asyncStorage', key: 'server-url', value: 'http://example.com' }],
      };

      const result = generateInterceptHtml(config);

      // Should contain meta tag with config
      expect(result).toContain('<meta name="bloud-idb-config" content="');
      expect(result).toContain('actual');
      expect(result).toContain('asyncStorage');
      expect(result).toContain('server-url');
    });

    it('generates script with intercept code', () => {
      const config: IndexedDBInterceptConfig = {
        database: 'db',
        intercepts: [{ store: 'store', key: 'key', value: 'value' }],
      };

      const result = generateInterceptHtml(config);

      // Should contain the intercept script
      expect(result).toContain('IDBObjectStore.prototype.get');
      expect(result).toContain('bloud-idb-config');
    });

    it('produces valid JSON in meta content after unescaping', () => {
      const config: IndexedDBInterceptConfig = {
        database: 'actual',
        intercepts: [
          { store: 'asyncStorage', key: 'server-url', value: 'http://localhost:8080/embed/actual-budget' },
        ],
      };

      const result = generateInterceptHtml(config);

      // Extract content from meta tag
      const contentMatch = result.match(/<meta name="bloud-idb-config" content="([^"]+)">/);
      expect(contentMatch).not.toBeNull();

      // Unescape HTML entities (browser does this automatically)
      const unescaped = contentMatch![1]
        .replace(/&quot;/g, '"')
        .replace(/&lt;/g, '<')
        .replace(/&gt;/g, '>')
        .replace(/&#39;/g, "'")
        .replace(/&amp;/g, '&');

      const parsed = JSON.parse(unescaped);
      expect(parsed.actual.asyncStorage['server-url']).toBe('http://localhost:8080/embed/actual-budget');
    });
  });

  describe('injectIntoHtml', () => {
    const testConfig: IndexedDBInterceptConfig = {
      database: 'testdb',
      intercepts: [{ store: 'store', key: 'key', value: 'value' }],
    };

    it('returns original HTML for null config', () => {
      const html = '<!DOCTYPE html><html><head></head><body></body></html>';
      const result = injectIntoHtml(html, null);
      expect(result).toBe(html);
    });

    it('returns original HTML for empty intercepts', () => {
      const html = '<!DOCTYPE html><html><head></head><body></body></html>';
      const emptyConfig: IndexedDBInterceptConfig = { database: 'db', intercepts: [] };
      const result = injectIntoHtml(html, emptyConfig);
      expect(result).toBe(html);
    });

    it('injects after <head> tag', () => {
      const html = '<!DOCTYPE html><html><head><title>Test</title></head><body></body></html>';
      const result = injectIntoHtml(html, testConfig);

      expect(result).toContain('<head><meta name="bloud-idb-config"');
      expect(result).toContain('</script><title>Test</title>');
    });

    it('handles <head> with attributes', () => {
      const html = '<!DOCTYPE html><html><head lang="en" data-foo="bar"><title>Test</title></head></html>';
      const result = injectIntoHtml(html, testConfig);

      expect(result).toContain('<head lang="en" data-foo="bar"><meta');
    });

    it('handles uppercase HEAD tag', () => {
      const html = '<!DOCTYPE html><html><HEAD><title>Test</title></HEAD></html>';
      const result = injectIntoHtml(html, testConfig);

      expect(result).toContain('<HEAD><meta');
    });

    it('handles mixed case head tag', () => {
      const html = '<!DOCTYPE html><html><Head><title>Test</title></Head></html>';
      const result = injectIntoHtml(html, testConfig);

      expect(result).toContain('<Head><meta');
    });

    it('falls back to after DOCTYPE when no head tag', () => {
      const html = '<!DOCTYPE html><html><body>No head tag</body></html>';
      const result = injectIntoHtml(html, testConfig);

      expect(result).toContain('<!DOCTYPE html><meta');
    });

    it('prepends when no DOCTYPE and no head tag', () => {
      const html = '<html><body>Minimal HTML</body></html>';
      const result = injectIntoHtml(html, testConfig);

      expect(result.startsWith('<meta')).toBe(true);
    });

    it('preserves rest of HTML structure', () => {
      const html = `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>App</title>
  <link rel="stylesheet" href="/style.css">
</head>
<body>
  <div id="app"></div>
  <script src="/app.js"></script>
</body>
</html>`;

      const result = injectIntoHtml(html, testConfig);

      // Should still contain all original content
      expect(result).toContain('<meta charset="utf-8">');
      expect(result).toContain('<title>App</title>');
      expect(result).toContain('<link rel="stylesheet" href="/style.css">');
      expect(result).toContain('<div id="app"></div>');
      expect(result).toContain('<script src="/app.js"></script>');
    });

    it('injection appears before other scripts', () => {
      const html = '<!DOCTYPE html><html><head><script src="/app.js"></script></head></html>';
      const result = injectIntoHtml(html, testConfig);

      const injectionIndex = result.indexOf('bloud-idb-config');
      const appScriptIndex = result.indexOf('src="/app.js"');

      expect(injectionIndex).toBeLessThan(appScriptIndex);
    });
  });

  describe('real-world scenarios', () => {
    it('handles Actual Budget config', () => {
      const config: IndexedDBInterceptConfig = {
        database: 'actual',
        intercepts: [
          { store: 'asyncStorage', key: 'server-url', value: 'http://localhost:8080/embed/actual-budget' },
        ],
      };

      const html = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Actual Budget</title>
</head>
<body>
  <div id="root"></div>
</body>
</html>`;

      const result = injectIntoHtml(html, config);

      // Verify injection
      expect(result).toContain('bloud-idb-config');
      expect(result).toContain('server-url'); // Quotes are escaped in HTML
      expect(result).toContain('actual-budget'); // URL is present (escaped)

      // Verify script runs before app
      const configIndex = result.indexOf('bloud-idb-config');
      const rootIndex = result.indexOf('id="root"');
      expect(configIndex).toBeLessThan(rootIndex);
    });

    it('handles HTML with existing inline scripts', () => {
      const config: IndexedDBInterceptConfig = {
        database: 'db',
        intercepts: [{ store: 's', key: 'k', value: 'v' }],
      };

      const html = `<html><head><script>var x = 1;</script></head></html>`;
      const result = injectIntoHtml(html, config);

      // Our injection should come first
      const ourScriptIndex = result.indexOf('bloud-idb-config');
      const existingScriptIndex = result.indexOf('var x = 1');
      expect(ourScriptIndex).toBeLessThan(existingScriptIndex);
    });

    it('handles special characters in values safely', () => {
      const config: IndexedDBInterceptConfig = {
        database: 'db',
        intercepts: [
          { store: 'store', key: 'html', value: '<script>alert("xss")</script>' },
        ],
      };

      const result = generateInterceptHtml(config);

      // Should use meta tag
      expect(result).toContain('<meta name="bloud-idb-config"');

      // Angle brackets should be escaped in attribute
      expect(result).not.toContain('content="<script>');
      expect(result).toContain('&lt;script&gt;');

      // Extract and verify the value is recoverable
      const contentMatch = result.match(/<meta name="bloud-idb-config" content="([^"]+)">/);
      expect(contentMatch).not.toBeNull();

      // Unescape and parse
      const unescaped = contentMatch![1]
        .replace(/&quot;/g, '"')
        .replace(/&lt;/g, '<')
        .replace(/&gt;/g, '>')
        .replace(/&#39;/g, "'")
        .replace(/&amp;/g, '&');

      const parsed = JSON.parse(unescaped);
      expect(parsed.db.store.html).toBe('<script>alert("xss")</script>');
    });
  });
});
