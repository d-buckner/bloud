<!-- Keep the `## Changelog` heading and its heading level: the CI check finds the
section by that heading, and it is the only copy of the release entry. -->

## What changes

Describe the change. One behavior per sentence.

## Why

State the problem this removes, or the goal it serves. Quote the exact error,
command, or string a reader must understand.

## Changelog

<!-- One bullet per user-visible change. Start each bullet with a category and a -->
<!-- colon. The categories are Added, Changed, Deprecated, Removed, Fixed, and -->
<!-- Security. -->
<!-- The text after the colon is a sentence: it appears in the release notes under -->
<!-- that category, next to the sentences from every other pull request. -->
<!-- Example: - Added: A searchable time zone picker under Settings. -->
<!-- Write n/a on its own line when nothing a user sees changes. -->
<!-- This section is the only copy of the entry. A squash merge leaves the commit -->
<!-- body on main empty, so a missing section loses the entry and the release notes -->
<!-- omit the change. `./bloud changelog --check-pr` fails the build on it. -->

## How this was verified

Name the command you ran, or say which check covers the change. If nothing
covers it, say so.
