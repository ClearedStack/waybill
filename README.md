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

> **Status: design.** The specification is not built. One first piece works: a command that compiles a single approved record for a calculator. See [What works today](#what-works-today) and [Not yet done](#not-yet-done).

Waybill is stage 05 of the ClearedStack pipeline. ClearedStack is an open pipeline that finds ideas for government-contracting tools, screens and scores them, and builds each one from the public rule. Waybill is meant to be understood and used without the rest.

## What works today

One command, for one kind of tool.

`waybill compile` reads one approved record (a JSON file) and, if the record asks for a **calculator**, gives back:

- the **credit note**: three lines, `Idea seen in`, `What we took`, `What we did differently`, filled into a fixed Go template;
- the **build specification**: JSON with exactly four parts, `provenance`, `authority`, `schema` and `vectors`.

Along the way it:

- checks every worked example against the formulas, with exact decimal arithmetic, and refuses the record if one disagrees;
- never reads the vendor material a record carries (`vendor_observations`), and refuses a record whose specification would still contain the product or vendor name, markup or a link outside the provenance block;
- stops instead of guessing: a tool type, value type, formula function, determination or contract version it has no rule for gives `E_NEEDS_SUBTOOL` and the state `needs_subtool`; broken data gives `E_PARSE`;
- gives the same bytes for the same record every time. It uses no clock, no network and no model.

A calculator here means: numeric inputs with a unit and bounds, numeric outputs, and for each output a formula using `+ - * /`, parentheses, `min` and `max`.

### Run it

Needs Go 1.22 or later. There are no dependencies outside the standard library.

```sh
go build -o waybill ./cmd/waybill

# print the result as JSON
./waybill compile tools/spec-profiles/calculator/testdata/ok-capped-allowance/record.json

# also write <item>.credit-note.txt and <item>.build-spec.json into a folder
./waybill compile -out /tmp/out tools/spec-profiles/calculator/testdata/ok-capped-allowance/record.json

# or read the record from standard input
./waybill compile < record.json
```

Exit codes: `0` compiled, `1` one of the three public errors (it is in the printed result), `2` wrong usage, `3` the output could not be written.

### Test it

```sh
go test ./...
```

The saved examples are in [`tools/spec-profiles/calculator/testdata`](tools/spec-profiles/calculator/testdata): each folder has a `record.json` and the exact output expected. All of the data is made up; the cited clauses and publications do not exist. After a deliberate change in behaviour, `go test ./tools/spec-profiles/calculator -update` rewrites the expected output; read the diff before committing it.

### The shape of a record

`SPEC.md` does not say what an approved record looks like, so the shape used here is a working assumption, defined in [`internal/record/record.go`](internal/record/record.go) and, for the calculator part, in [`tools/spec-profiles/calculator/build.go`](tools/spec-profiles/calculator/build.go). The wording of the credit note is likewise this repository’s own, in [`internal/credit/credit-note.tmpl`](internal/credit/credit-note.tmpl). Both need to be agreed before anything depends on them.

## Not yet done

Everything else in `SPEC.md`:

- **Muster.** No leasing, no priority lanes, no acknowledgement, no interface and no in-memory stand-in yet. The command reads a file.
- **`E_TIMEOUT`**, the spool at `/var/spool/cs` and retrying until acknowledged. The code exists as a name only.
- **Opt-outs and corrections.** No neutral wording, no 48-hour or 5-day clocks, no republishing. A record whose determination is not `build` is paused as `needs_subtool`.
- **Reporting.** No fingerprinted errors to Bellbuoy and no replay of paused items.
- **Other tool types.** `calculator` is the only spec-profile. Calculators that need rounding, conditions, lookups or non-numeric values are paused as `needs_subtool`.
- **Running as a worker.** No long-running process, no `ko` build configuration, no Kubernetes manifests, no network policy. The 30 MiB budget, the read-only filesystem and the unprivileged user have not been tested in a cluster.

## In this repository

| File | What it is |
| --- | --- |
| [`SPEC.md`](SPEC.md) | The specification: what Waybill does, its interfaces, how it fails and how it is kept safe. |
| [`PROMPT.md`](PROMPT.md) | The prompt to start a build session with. It says what to build and the rules to build it under. |
| [`component.json`](component.json) | This component’s entry in the ClearedStack naming registry. |
| [`brand/`](brand) | The Waybill logo and mark, for light and dark backgrounds. |
| [`cmd/waybill`](cmd/waybill) | The command. |
| [`internal/`](internal) | The record, the credit note, the build specification and the compile step. |
| [`tools/`](tools) | The sub-tools. [`tools/registry.yaml`](tools/registry.yaml) lists them; `spec-profiles` has one profile, `calculator`. |

## At a glance

| | |
| --- | --- |
| **Stage** | 05 of 10 in the pipeline |
| **Formerly** | `cs-05-provenance` |
| **Namespace** | `cs-workers` |
| **Container image** | `lazarette.clearedstack.svc.cluster.local:5000/waybill` |
| **Memory budget** | 30 MiB |
| **Depends on** | [Muster](https://github.com/ClearedStack/muster) |
| **Depended on by** | Nothing else in the platform |
| **Rules for particular cases** | `spec-profiles` |

### Boundaries

- Leases approved items from Muster
- Emits the credit note and the build specification

## Names

`waybill` is this component’s identifier everywhere: the repository, the workload, the container image. The one place names are written down is the [naming registry](https://clearedstack.com/platform/registry.json), kept in [`clearedstack.com`](https://github.com/ClearedStack/clearedstack.com/blob/main/src/data/registry.json). `component.json` here is a copy of this component’s entry. If the two disagree, the registry is right.

## Ground rules

- **No AI when it runs.** The same input gives the same output on any machine.
- **Stop instead of guessing.** A case with no rule is paused and reported, never improvised.
- **Clean room.** Written from scratch. No code, text or design is copied from another project.
- **Public sources and made-up data only.** Nothing classified, controlled or taken from a real contract, in code, tests, issues or commit messages.

## License

[MIT](LICENSE). ClearedStack is an independent personal project. It is not affiliated with, endorsed by, or sponsored by the U.S. Department of Defense or any other government agency.
