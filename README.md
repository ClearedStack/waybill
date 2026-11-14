<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/logo-dark.svg">
    <img src="brand/logo-light.svg" alt="Waybill" height="56">
  </picture>
</p>

<p align="center">
  <strong>Credits where an idea came from and writes the specification a tool is built from.</strong><br>
  Part of <a href="https://clearedstack.com">ClearedStack</a>
</p>

---

> **Status: ready to deploy.** Waybill speaks Muster contract v1 (`ClearedStack/muster/contract/v1/CONTRACT.md`), replays all 52 saved Muster contract examples, compiles all four `spec-profiles` (`calculator`, `checklist`, `validator`, `decision-tree`), stages outcomes atomically in `/var/spool/cs`, and ships hardened Kubernetes manifests under [`deploy/`](deploy).

Waybill is stage 05 of the ClearedStack pipeline. ClearedStack is an open pipeline that finds ideas for government-contracting tools, screens and scores them, and builds each one from the public rule. Waybill can run either as a standalone compiler or as an in-cluster Muster worker, and can also run locally against its built-in Muster contract v1 stand-in without a cluster.

## What works

1. **Muster contract v1 client and worker loop (`waybill run`):**
   - Calls `POST /v1/lease`, `/v1/heartbeat`, `/v1/complete`, `/v1/fail`, and `/v1/needs_subtool` with `CONTRACT_VERSION=v1` and projected Kubernetes ServiceAccount bearer tokens.
   - Replays every saved example in [`testdata/contract/v1/examples/`](testdata/contract/v1/examples) (copied verbatim from `ClearedStack/muster/contract/v1/examples/`).
   - Drains priority lanes in Muster's order (`urgent` -> `normal` -> `backfill`), handling both initial `approved -> approved` items and `building -> building` rebuilds.
   - Stages every outcome (`complete`, `needs_subtool`, `fail`) atomically in `/var/spool/cs/outbox` before acknowledging Muster, surviving `503` store outages and pod restarts without losing compiled specifications.

2. **Clean-room wall, provenance stamping, and 48h/5d SLAs:**
   - Ignores `found` and all upstream observation results (`results.trawl`, `results.sextant`, `results.assay`), reading only `results.countersign` and rejecting any payload containing raw observation keys (`html`, `raw_html`, `screenshot`, `snapshot`, `observation`, etc.).
   - Renders deterministic three-line Go `text/template` credit notes ([`internal/credit/credit-note.tmpl`](internal/credit/credit-note.tmpl)) or verified opt-out neutral notes ([`internal/credit/credit-note-neutral.tmpl`](internal/credit/credit-note-neutral.tmpl)) that strip product and vendor names across the entire output while preserving statutory citations.
   - Evaluates 48-hour identity verification and 5-day opt-out / correction resolution SLAs purely from input timestamps without reading the system clock.

3. **Four deterministic `spec-profiles` sub-tools:**
   - [`calculator`](tools/spec-profiles/calculator): bounded exact-decimal arithmetic (`+ - * /`, parentheses, `min`, `max`), lowered to Moldloft's step-by-step half-away-from-zero decimal representation and verified on every worked example.
   - [`checklist`](tools/spec-profiles/checklist): compliance checklists (`yes`, `no`, optional `na` per item) with verified required-item and status counts.
   - [`validator`](tools/spec-profiles/validator): delimited schedule and line-item validators (`comma`, `pipe`, `colon`, `tab`) with `text`, `digits`, `upper_code`, and `in_table` lookup rules verified row-by-row against worked examples.
   - [`decision-tree`](tools/spec-profiles/decision-tree): acyclic branching determination graphs verified for cycle freedom, full question and outcome reachability from `start`, and exact branch evaluation.

4. **Downstream `results.waybill` compatibility (`<= 60 KiB`):**
   - Emits `{slug, entry, provenance, build_spec, spec, sla?}` under `results.waybill`, matching Moldloft's `UnwrapWaybillResult` (`spec`), Chartroom's catalog reader (`slug`, `entry`, `provenance` within 64 KiB), and Tidegauge's source-link monitor (`provenance.links`), while staying well within Logbook and Muster's 256 KiB JSONB bound.

### Build and run

Needs Go 1.24 or later. There are zero dependencies outside the Go standard library.

```sh
go build -o waybill ./cmd/waybill

# Standalone compilation of one approved record
./waybill compile tools/spec-profiles/calculator/testdata/ok-capped-allowance/record.json
./waybill compile -out /tmp/out tools/spec-profiles/checklist/testdata/ok-solicitation-checklist/record.json

# Run one worker pass against the built-in Muster contract v1 stand-in
./waybill run -standin -once -spool /tmp/waybill-spool

# Run the in-cluster worker against Muster
CONTRACT_VERSION=v1 MUSTER_URL=http://muster.clearedstack.svc.cluster.local:8080 \
  ./waybill run
```

### Verify and test

```sh
go vet ./...
go test -race ./...
```

All test data is synthetic; cited clauses (`FAR 97.x`, `DFARS 297.x`, `NIST SP 800-997`) and vendor names do not exist.

## What Waybill needs from other components

- **Countersign (`results.countersign`):** Must include the approved Waybill record under `results.countersign.waybill` (or `results.countersign.record`) when completing `scored -> approved`, carrying `source` (`product`, `vendor`), `determination` (`decision`, optional `opt_out`, `neutral_descriptor`, and RFC3339 UTC SLA timestamps), `title`, `authority` (`id`, `source`, `citation`, `title`, and at least one official public `https://` `url`), `tool_type`, and `tool`. Currently `ClearedStack/countersign` writes only decision receipt metadata (`outcome`, `reason`, etc.), which Waybill refuses fail-closed (`E_PARSE`) until Countersign attaches the structured Waybill input.
- **Muster (`contract/v1/CONTRACT.md`):** Must run in namespace `clearedstack` with ServiceAccount token verification allowing `cs-workers:waybill`. Note that `CONTRACT.md` restricts `POST /v1/needs_subtool` to `recorded`, `observed`, and `approved` (not `building`).
- **Keelworks, Lazarette, and Shipkeeper:** Keelworks builds `./cmd/waybill` via [`.ko.yaml`](.ko.yaml) for `linux/arm64`, pushes and signs the image in Lazarette, and replaces the placeholder `@sha256:0000...` digest in [`deploy/waybill.yaml`](deploy/waybill.yaml) before Shipkeeper applies `deploy/`.

## In this repository

| File | What it is |
| --- | --- |
| [`SPEC.md`](SPEC.md) | Technical specification, interfaces, failure modes, and contract v1 alignment notes. |
| [`PROMPT.md`](PROMPT.md) | Build prompt and platform rules. |
| [`component.json`](component.json) | Waybill's entry in the ClearedStack naming registry. |
| [`.ko.yaml`](.ko.yaml) | `ko` build configuration (no Dockerfile, `CGO_ENABLED=0`, `linux/arm64`, digest-pinned static base image). |
| [`deploy/`](deploy) | Hardened `cs-workers` `ServiceAccount`, `Deployment`, and default-deny `NetworkPolicy` manifests. |
| [`cmd/waybill`](cmd/waybill) | CLI entrypoint (`compile`, `run`, `version`). |
| [`internal/muster`](internal/muster) | Muster contract v1 HTTP client, stand-in server, and 52-example contract replay test. |
| [`internal/spool`](internal/spool) | Atomic `/var/spool/cs` outbox staging and crash recovery. |
| [`internal/worker`](internal/worker) | Priority-lane lease loop, heartbeats, clean-room extraction, and outbox delivery. |
| [`internal/record`](internal/record) | Input record parser, `PA-0001` ID validation, URL checks, and 48h/5d SLA computation. |
| [`internal/credit`](internal/credit) | Nominative and opt-out neutral Go `text/template` credit-note rendering. |
| [`internal/compile`](internal/compile) | Pure deterministic compiler and clean-room wall backstop. |
| [`tools/spec-profiles`](tools/spec-profiles) | Registered tool-type compilers (`calculator`, `checklist`, `validator`, `decision-tree`) and golden fixtures. |
| [`testdata/contract/v1/examples`](testdata/contract/v1/examples) | Saved Muster contract v1 examples replayed by `internal/muster`. |

## License

[MIT](LICENSE). ClearedStack is an independent personal project. It is not affiliated with, endorsed by, or sponsored by the U.S. Department of Defense or any other government agency.
