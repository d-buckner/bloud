"""
Upstream ICS feed syncing and filtering.

Fetches remote ICS feeds, filters events by patterns,
and detects changes via per-event content hashes.
Polling runs in a background thread at a configurable interval.
"""

import copy
import hashlib
import json
import re
import threading
from typing import Callable, Dict, List, Optional, Tuple
from urllib.error import URLError
from urllib.request import Request, urlopen

import vobject
from radicale.log import logger
from radicale_ics_sync import __version__

# Fields that determine if an event has meaningfully changed.
_RELEVANT_FIELDS = ("SUMMARY", "DTSTART", "DTEND", "LOCATION", "DESCRIPTION")


def _fetch_raw(url: str) -> Optional[str]:
    """Fetch raw text from a URL. Returns None on failure."""
    try:
        req = Request(url, headers={"User-Agent": f"radicale-ics-sync/{__version__}"})
        with urlopen(req, timeout=30) as response:
            charset = response.headers.get_content_charset("utf-8")
            return response.read().decode(charset)
    except URLError as e:
        logger.warning("radicale-ics-sync: failed to fetch %r: %s", url, e)
        return None
    except Exception as e:
        logger.warning("radicale-ics-sync: unexpected error fetching %r: %s", url, e)
        return None


def _event_content_hash(component: vobject.base.Component) -> str:
    """Compute a stable hash of an event's meaningful fields."""
    fields = {}
    for field in _RELEVANT_FIELDS:
        field_list = component.contents.get(field.lower(), [])
        fields[field] = str(field_list[0].value) if field_list else ""
    stable = json.dumps(fields, sort_keys=True)
    return hashlib.sha256(stable.encode()).hexdigest()


def _matches_any(summary: str, patterns: List[re.Pattern]) -> bool:
    """Return True if summary matches any of the compiled patterns."""
    return any(p.search(summary) for p in patterns)


def _filter_events(
    components: List[vobject.base.Component],
    include_patterns: List[re.Pattern],
    exclude_patterns: List[re.Pattern],
) -> List[vobject.base.Component]:
    """Filter VEVENT components by include and exclude patterns on SUMMARY."""
    result = []
    for component in components:
        try:
            summary = component.summary.value
        except AttributeError:
            summary = ""

        if include_patterns and not _matches_any(summary, include_patterns):
            continue
        if exclude_patterns and _matches_any(summary, exclude_patterns):
            continue
        result.append(component)
    return result


def _parse_events(
    ics_text: str,
    include_patterns: Optional[List[re.Pattern]] = None,
    exclude_patterns: Optional[List[re.Pattern]] = None,
) -> Optional[Tuple[Dict[str, str], Dict[str, str]]]:
    """Parse the content of an ICS file into per-event ICS text and per-event content hashes.
    Applies filtering before hashing.

    Returns None on parse failure.
    Returns {uid: ics_text}, {uid: content_hash} on success.
    """
    events: Dict[str, str] = {}
    hashes: Dict[str, str] = {}
    try:
        calendar = vobject.readOne(ics_text)
    except Exception as e:
        logger.error("radicale-ics-sync: failed to parse ICS: %s", e)
        return None

    all_components = list(calendar.components())
    timezones = [c for c in all_components if c.name == "VTIMEZONE"]
    components = [c for c in all_components if c.name == "VEVENT"]

    # Apply filtering
    filtered = _filter_events(
        components,
        include_patterns or [],
        exclude_patterns or [],
    )

    if include_patterns or exclude_patterns:
        logger.info(
            "radicale-ics-sync: %d/%d events passed filter",
            len(filtered),
            len(components),
        )

    for component in filtered:
        try:
            uid = component.uid.value
        except AttributeError:
            uid = hashlib.sha256(component.serialize().encode()).hexdigest()
            component.add("uid").value = uid

        wrapper = vobject.iCalendar()
        for tz in timezones:
            wrapper.add(copy.deepcopy(tz))
        wrapper.add(component)
        events[uid] = wrapper.serialize()
        hashes[uid] = _event_content_hash(component)

    return events, hashes


def _feed_content_hash(hashes: Dict[str, str]) -> str:
    """Compute a stable hash of the entire feed from per-event hashes."""
    stable = json.dumps(hashes, sort_keys=True)
    return hashlib.sha256(stable.encode()).hexdigest()


class SyncJob:
    """Manages periodic polling of a single upstream ICS feed URL."""

    def __init__(
        self,
        url: str,
        interval_seconds: int,
        on_update: Callable[[Dict[str, str], Dict[str, str]], None],
        include_patterns: Optional[List[re.Pattern]] = None,
        exclude_patterns: Optional[List[re.Pattern]] = None,
    ) -> None:
        self._url = url
        self._interval = interval_seconds
        self._on_update = on_update
        self._include_patterns = include_patterns or []
        self._exclude_patterns = exclude_patterns or []
        self._last_feed_hash: Optional[str] = None
        self._thread: Optional[threading.Thread] = None
        self._stop_event = threading.Event()

    def fetch_once(self) -> None:
        """Fetch the feed once and call on_update if content changed."""
        logger.info("radicale-ics-sync: fetching upstream feed %r", self._url)
        raw = _fetch_raw(self._url)
        if raw is None:
            return

        result = _parse_events(raw, self._include_patterns, self._exclude_patterns)
        if result is None:
            return
        events, hashes = result

        feed_hash = _feed_content_hash(hashes)
        if feed_hash == self._last_feed_hash:
            logger.info("radicale-ics-sync: feed unchanged, skipping")
            return

        logger.info("radicale-ics-sync: feed updated, %d events", len(events))
        self._last_feed_hash = feed_hash
        self._on_update(events, hashes)

    def start(self) -> None:
        """Start background polling thread."""
        self._thread = threading.Thread(
            target=self._poll_loop, daemon=True, name="ics-sync-poller"
        )
        self._thread.start()
        logger.info(
            "radicale-ics-sync: polling %r every %ds", self._url, self._interval
        )

    def stop(self) -> None:
        """Stop background polling thread."""
        self._stop_event.set()

    def _poll_loop(self) -> None:
        self.fetch_once()
        while not self._stop_event.wait(timeout=self._interval):
            self.fetch_once()
