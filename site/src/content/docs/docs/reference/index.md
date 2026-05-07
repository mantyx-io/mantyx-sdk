---
title: Reference
description: Per-SDK reference docs — TypeScript, Go, and Python.
sidebar:
  hidden: true
---

The reference for each SDK is mirrored from the per-package README at build time:

- [TypeScript (`@mantyx/sdk`)](/docs/reference/typescript/)
- [Go (`mantyx-sdk/go`)](/docs/reference/go/)
- [Python (`mantyx-sdk`)](/docs/reference/python/)

The protocol specs — what every third-party client must implement — live at:

- [Agent-runs protocol](/docs/protocol/) — HTTP routes, auth, body shapes, sessions, error codes.
- [Wire protocol — messaging & data structures](/docs/wire-protocol/) — every SSE event and resolved-content blob (A2A Agent Card, MCP `Tool[]`, `Implementation`) the SDK exchanges with MANTYX during a run.
