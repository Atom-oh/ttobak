---
name: refactor
description: Refactor the requested TTOBAK scope while preserving verified contracts and running relevant checks.
---

# Refactor

Read the affected implementation and root project guide. Preserve existing API,
key-schema, authorization and concurrency contracts unless the task explicitly
includes changing them. Keep changes focused and verify behavior with the root
commands: Go tests include command packages; frontend requires lint/build; infra
requires synth/Jest. Update only affected current references and regenerate review
context after canonical guidance changes. Historical plans do not authorize
unrequested refactors or supersede current constraints.
