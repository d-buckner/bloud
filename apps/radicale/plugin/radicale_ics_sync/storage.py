"""
Radicale ICS Sync - Storage Plugin

Wraps Radicale's multifilesystem storage and syncs upstream ICS feeds
into Radicale collections with event filtering.
Upstream changes are detected via event content hashes.
Newer upstream changes overwrite local changes.

Configuration is read from the path set by ics_config in [storage]
(default: /config/ics_sync.json).
"""

import json
import os
import re
from typing import Dict, List

import radicale.item as radicale_item
from radicale.log import logger
from radicale.storage import BaseStorage, ComponentNotFoundError
from radicale.storage.multifilesystem import Storage as MultiFileSystemStorage

from .upstream import SyncJob

_DEFAULT_INTERVAL = 3600
_DEFAULT_CONFIG_PATH = "/config/ics_sync.json"
_BATCH_SIZE = 20

PLUGIN_CONFIG_SCHEMA = {
    "storage": {
        "ics_config": {
            "value": _DEFAULT_CONFIG_PATH,
            "help": "path to the ics_sync.json configuration file",
            "type": str,
        },
        # Bloud addition: upstream always persists the hash database next to
        # filesystem_folder, which in this image is outside the mounted tree and
        # is lost on every container recreate. Pointing it inside the persisted
        # collections folder is what keeps deletions working across restarts (a
        # fresh hash db re-uploads every event and never removes the ones that
        # disappeared upstream).
        "hash_db": {
            "value": "",
            "help": "path to the persisted upstream hash database "
                    "(default: next to filesystem_folder)",
            "type": str,
        },
    }
}


def _load_hashes(path: str) -> Dict[str, Dict[str, Dict[str, str]]]:
    """Load persisted upstream hashes from disk.

    Returns {collection_path: {feed_url: {uid: hash}}}.
    """
    try:
        with open(path, "r", encoding="utf-8") as f:
            data = json.load(f)
            total = sum(
                len(uids)
                for feed_dicts in data.values()
                for uids in feed_dicts.values()
            )
            logger.info(
                "radicale-ics-sync: loaded %d upstream hashes across %d collections from %s",
                total,
                len(data),
                path,
            )
            return data
    except FileNotFoundError:
        logger.info("radicale-ics-sync: no hash db found at %s, starting fresh", path)
        return {}
    except Exception as e:
        logger.warning(
            "radicale-ics-sync: failed to load hash db: %s, starting fresh", e
        )
        return {}


def _save_hashes(hashes: Dict[str, Dict[str, Dict[str, str]]], path: str) -> None:
    """Persist upstream hashes to disk."""
    try:
        with open(path, "w", encoding="utf-8") as f:
            json.dump(hashes, f, indent=2)
    except Exception as e:
        logger.warning("radicale-ics-sync: failed to save hash db: %s", e)


def _load_sync_config(config_path: str) -> List[dict]:
    """Load the configuration file."""
    try:
        with open(config_path, "r", encoding="utf-8") as f:
            data = json.load(f)
            if not isinstance(data, list):
                logger.error("radicale-ics-sync: %s must be a JSON array", config_path)
                return []
            logger.info(
                "radicale-ics-sync: loaded %d sync job(s) from %s",
                len(data),
                config_path,
            )
            return data
    except FileNotFoundError:
        logger.info(
            "radicale-ics-sync: no config found at %s, running in passthrough mode",
            config_path,
        )
        return []
    except Exception as e:
        logger.error("radicale-ics-sync: failed to load config %s: %s", config_path, e)
        return []


def _compile_patterns(patterns: List[str]) -> List[re.Pattern]:
    """Compile a list of regex pattern strings, case-insensitive.

    Invalid patterns are skipped with a warning.
    """
    compiled = []
    for pattern in patterns:
        try:
            compiled.append(re.compile(pattern, re.IGNORECASE))
        except re.error as e:
            logger.warning("radicale-ics-sync: invalid pattern %r: %s", pattern, e)
    return compiled


class Storage(BaseStorage):
    """ICS Sync storage plugin.

    Delegates all standard CalDAV operations to Radicale's built-in
    multifilesystem storage, while adding upstream ICS feed syncing on top.
    """

    def __init__(self, configuration):
        super().__init__(configuration.copy(PLUGIN_CONFIG_SCHEMA))
        self._delegate = MultiFileSystemStorage(configuration)
        self._sync_jobs: List[SyncJob] = []
        filesystem_folder = configuration.get("storage", "filesystem_folder")
        self._hash_db_path = os.path.join(
            os.path.dirname(os.path.normpath(filesystem_folder)),
            "ics_sync_hashes.json",
        )
        self._upstream_hashes: Dict[str, Dict[str, Dict[str, str]]] = _load_hashes(
            self._hash_db_path
        )
        self._setup_sync_jobs(self.configuration)
        logger.info("radicale-ics-sync: storage plugin loaded")

    def _setup_sync_jobs(self, configuration) -> None:
        """Read sync jobs from the config file and start a thread for each."""
        config_path = configuration.get("storage", "ics_config")
        sync_jobs = _load_sync_config(config_path)
        if not sync_jobs:
            return

        for job in sync_jobs:
            feed_url = job.get("feed")
            collection_path = job.get("collection")
            display_name = job.get("displayname", "")

            if not feed_url:
                logger.error("radicale-ics-sync: sync job missing 'feed': %s", job)
                continue
            if not collection_path:
                logger.error(
                    "radicale-ics-sync: sync job missing 'collection': %s", job
                )
                continue

            interval = job.get("sync_interval", _DEFAULT_INTERVAL)
            include_patterns = _compile_patterns(job.get("include_patterns", []))
            exclude_patterns = _compile_patterns(job.get("exclude_patterns", []))

            if include_patterns:
                logger.info(
                    "radicale-ics-sync: [%s] include patterns: %s",
                    collection_path,
                    [p.pattern for p in include_patterns],
                )
            if exclude_patterns:
                logger.info(
                    "radicale-ics-sync: [%s] exclude patterns: %s",
                    collection_path,
                    [p.pattern for p in exclude_patterns],
                )

            sync_job = SyncJob(
                url=feed_url,
                interval_seconds=interval,
                on_update=lambda events, hashes, p=collection_path, u=feed_url, d=display_name: (
                    self._sync_events(p, u, events, hashes, d)
                ),
                include_patterns=include_patterns,
                exclude_patterns=exclude_patterns,
            )
            sync_job.start()
            self._sync_jobs.append(sync_job)
            logger.info(
                "radicale-ics-sync: registered sync job: %s → %s (every %ds)",
                feed_url,
                collection_path,
                interval,
            )

    def _sync_events(
        self,
        collection_path: str,
        feed_url: str,
        events: Dict[str, str],
        hashes: Dict[str, str],
        display_name: str = "",
    ) -> None:
        """Sync upstream events to Radicale for a specific feed + collection.

        Deletes and writes are processed in batches of _BATCH_SIZE so that the
        write lock is released between batches, allowing Radicale to serve
        CalDAV requests in the gaps.
        """
        path = "/" + collection_path.strip("/")

        # Phase 1: verify the collection exists and compute both work lists.
        with self._delegate.acquire_lock("w"):
            collections = self._ensure_collection(path, collection_path, display_name)
            if not collections:
                return

            collection_feeds = self._upstream_hashes.setdefault(collection_path, {})
            feed_hashes = collection_feeds.setdefault(feed_url, {})

            to_delete = list(set(feed_hashes.keys()) - set(events.keys()))
            to_write = [
                (uid, ics_text)
                for uid, ics_text in events.items()
                if feed_hashes.get(uid) != hashes[uid]
            ]
            skipped = len(events) - len(to_write)

        # Phase 2: delete stale events in batches.
        deleted = 0
        for i in range(0, len(to_delete), _BATCH_SIZE):
            batch = to_delete[i : i + _BATCH_SIZE]
            with self._delegate.acquire_lock("w"):
                collections = list(self._delegate.discover(path, depth="0"))
                if not collections:
                    logger.warning(
                        "radicale-ics-sync: collection %r disappeared during sync, "
                        "aborting",
                        collection_path,
                    )
                    return
                collection = collections[0]

                for uid in batch:
                    href = uid + ".ics"
                    try:
                        collection.delete(href)
                        del feed_hashes[uid]
                        deleted += 1
                        logger.debug("radicale-ics-sync: deleted event %r", uid)
                    except ComponentNotFoundError:
                        del feed_hashes[uid]
                        logger.debug(
                            "radicale-ics-sync: event %r already gone, skipping", uid
                        )
                    except Exception as e:
                        logger.warning(
                            "radicale-ics-sync: failed to delete event %r: %s", uid, e
                        )

                _save_hashes(self._upstream_hashes, self._hash_db_path)

        # Phase 3: write new/changed events in batches.
        written = 0
        for i in range(0, len(to_write), _BATCH_SIZE):
            batch = to_write[i : i + _BATCH_SIZE]
            with self._delegate.acquire_lock("w"):
                collections = list(self._delegate.discover(path, depth="0"))
                if not collections:
                    logger.warning(
                        "radicale-ics-sync: collection %r disappeared during sync, "
                        "aborting",
                        collection_path,
                    )
                    return
                collection = collections[0]

                for uid, ics_text in batch:
                    href = uid + ".ics"
                    try:
                        item = radicale_item.Item(collection=collection, text=ics_text)
                        collection.upload(href, item)
                        feed_hashes[uid] = hashes[uid]
                        written += 1
                    except Exception as e:
                        logger.warning(
                            "radicale-ics-sync: failed to write event %r: %s", uid, e
                        )

                _save_hashes(self._upstream_hashes, self._hash_db_path)

        logger.info(
            "radicale-ics-sync: [%s] wrote %d, deleted %d, skipped %d unchanged",
            collection_path,
            written,
            deleted,
            skipped,
        )

    def _ensure_collection(
        self, path: str, collection_path: str, display_name: str
    ) -> List:
        """Return the target collection, creating it when it does not exist yet.

        Bloud addition: upstream requires the collection to pre-exist and skips
        the feed otherwise, telling the operator to create it in the web UI.
        Bloud cannot do that over the DAV API: the calendar belongs to a Bloud
        user whose password the host agent never holds, so no client credential
        exists to MKCALENDAR with. The storage layer is where the plugin already
        runs and already has write access, so it creates the calendar itself.
        """
        collections = list(self._delegate.discover(path, depth="0"))
        if collections:
            return collections
        props = {"tag": "VCALENDAR"}
        if display_name:
            props["D:displayname"] = display_name
        logger.info("radicale-ics-sync: creating collection %r", collection_path)
        try:
            self._delegate.create_collection(collection_path, props=props)
        except Exception as e:
            logger.warning(
                "radicale-ics-sync: failed to create collection %r: %s",
                collection_path,
                e,
            )
            return []
        return list(self._delegate.discover(path, depth="0"))

    def discover(self, path, depth="0", child_context_manager=None, user_groups=None):
        return self._delegate.discover(
            path, depth, child_context_manager, user_groups or set()
        )

    def move(self, item, to_collection, to_href):
        return self._delegate.move(item, to_collection, to_href)

    def create_collection(self, href, items=None, props=None):
        return self._delegate.create_collection(href, items, props)

    def acquire_lock(self, mode, user="", *args, **kwargs):
        return self._delegate.acquire_lock(mode, user, *args, **kwargs)

    def verify(self):
        return self._delegate.verify()
