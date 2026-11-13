# Waybill specification

> Technical specification for the deterministic Waybill worker that stamps Go-template provenance notes, enforces 48h/5d SLAs, and compiles sanitized JSON build specs.

**Status: design.** This is a specification. It describes how Waybill is meant to work, not something that exists yet.

The same text is published at [clearedstack.com/platform/waybill/](https://clearedstack.com/platform/waybill/). To change the specification, change it here and say why.

## Overview & Role

Waybill (`v0.1.0-draft`) is the deterministic attribution, SLA-enforcement, and clean-room specification compiler in ClearedStack v3.1, replacing the original Stage 7 (Credit). Implemented in the Waybill repository under the MIT License, it runs with zero AI models so provenance phrasing and build specifications are 100% repeatable.

Its core responsibilities are:

- **Deterministic provenance stamping:** Render standardized attribution blocks using Go `text/template` files with three factual lines (`Idea seen in`, `What we took`, `What we did differently`).
- **48-hour and 5-day SLA enforcement:** Track identity verification within 48 hours and honor nominative attribution opt-outs within 5 days by replacing product/vendor names with neutral descriptors while keeping public-domain law citations intact.
- **Clean-room build spec compilation:** Compile a sanitized JSON build spec (`provenance`, `authority`, `schema`, `vectors`)—the sole artifact permitted to cross the clean-room wall into Moldloft.

## Runtime & Resource Budget

Waybill runs in the `cs-workers` Kubernetes namespace under the **Standard (Workers)** priority class.

| Attribute | Value |
| --- | --- |
| **Worker ID / Repo** | Waybill (`waybill`) |
| **Namespace** | `cs-workers` |
| **Kind / Version** | `worker` · `v0.1.0-draft` |
| **RAM Budget** | `30 MiB` (always-on Go pod) |
| **Runtime AI Calls** | `0` (100% deterministic Go `text/template` + spec compilers) |
| **Local Spool** | `/var/spool/cs` (persists compiled JSON specs across Muster restarts) |
| **Build & Deploy** | Built with `ko` (no Docker) by Keelworks, signed with `cosign` in Lazarette, deployed by Shipkeeper via Server-Side Apply |

## Interfaces & State Transitions

Waybill leases work from Muster in the `clearedstack` namespace over ServiceAccount-authenticated HTTP (`CONTRACT_VERSION=v1`), draining `urgent` → `normal` → `backfill` priority lanes.

- **Inbound state:** `approved` (items approved for **Build** by the owner in Countersign, plus opt-out or factual correction requests routed through Countersign).
- **Processing steps:**
  1. Load the Countersign determination and official statutory citations (FAR, DFARS, NIST, eCFR).
  2. Render the Go-template provenance block (or apply neutral phrasing if an opt-out is active).
  3. Match the item to a `tools/spec-profiles/<tool-type>/` sub-tool.
  4. Compile the sanitized JSON build spec (`provenance`, `authority`, `schema`, `vectors`), resolving any conflict between vendor behavior and official government publications strictly in favor of the government text.
- **Outbound transitions:**
  - `building` — Sanitized JSON build spec compiled and validated; item is ready for Moldloft.
  - `needs_subtool` — Fail-closed pause (`E_NEEDS_SUBTOOL`) when no `spec-profiles` sub-tool matches the required tool structure.
  - Opt-out / correction updates on `live` tools trigger Chartroom republishing within the 5-day SLA.

## Sub-Tools & Failure Modes

Tool-type specification compilers live inside Waybill under `tools/spec-profiles/<tool-type>/`, each with matching rules, deterministic Go spec-builder logic, and golden test fixtures.

| Sub-Tool | Responsibility |
| --- | --- |
| `spec-profiles` | Tool-type compilers that translate approved regulatory citations, typed inputs/outputs, units, validation bounds, formulas, and public worked examples into sanitized JSON build specs. |

### Sanitized Failure Modes

| Error Code | Trigger | Platform Behavior |
| --- | --- | --- |
| `E_NEEDS_SUBTOOL` | Approved item requires a tool structure not covered by `tools/spec-profiles/<tool-type>/`. | Fails closed to `needs_subtool`. Bellbuoy opens a deduplicated task in Waybill on Plainforge; once the new profile and golden fixture deploy, Muster auto-replays paused items. |
| `E_PARSE` | Missing statutory authority, malformed worked-example vectors, or invalid spec schema. | Halts compilation cleanly and emits a fingerprinted error via Bellbuoy. |
| `E_TIMEOUT` | Transient timeout communicating with Muster. | Compiled spec is held in `/var/spool/cs` and retried until acknowledged. |

## Security & Clean-Room Boundaries

- **Enforces the clean-room wall before Moldloft:** Waybill strips all raw HTML snapshots, vendor marketing URLs inside build inputs, vendor field labels, CSS classes, proprietary schemas, and scraped vendor test data. Only functional specifications anchored to official government publications and public worked examples cross into Moldloft.
- **Nominative attribution rules:** Provenance notes never rate, criticize, or disparage observed products, and never imply vendor partnership, sponsorship, or endorsement.
- **Pod hardening & network policy:** Runs non-root with dropped capabilities, read-only root filesystem (writable only at `/var/spool/cs`), zero database credentials, and default-deny `NetworkPolicy` allowing only ServiceAccount HTTP to Muster.

## Related pages

- [Clean room and credit](https://clearedstack.com/platform/clean-room-and-credit/)
- [The pipeline and its ten workers](https://clearedstack.com/platform/pipeline/#building-verifying-and-listing)
- [An item’s life: fourteen states and three lanes](https://clearedstack.com/platform/item-lifecycle/)
- [The pipeline and its ten workers](https://clearedstack.com/platform/pipeline/)
