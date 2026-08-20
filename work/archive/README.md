# work/archive

Actioned working documents — designs, drafts, research and reviews whose work shipped. They are
records, not requirements: nothing in here asks for anything. Each file carries an
`> ARCHIVED <date>` stamp naming the evidence, and this index says in one line what each was and
where its substance went. Flat, filenames preserved. Append entries; never rewrite others'.

| File | What it was | Archived | Where the substance went |
|---|---|---|---|
| `mesh.md` | The Benzene Mesh design (Phases 1–5, with the 2026-08 supersession notes) | 2026-08-20 | Shipped as `mesh/`, `meshd/`, `examples/mesh-helloworld`; contracts normative in the main repo's `docs/specification/mesh.md`, pinned by the vendored `conformance/testdata/mesh-*-cases.json` |
| `mesh-spec-draft.md` | The draft mesh wire contracts authored for promotion to the main repo | 2026-08-20 | Promoted and merged as the main repo's `docs/specification/mesh.md`; fixtures vendored back here and passing |
| `mesh-research.md` | Research and positioning behind the mesh design (sources, incumbents, thesis) | 2026-08-20 | The design it argued for shipped in full; kept as source material for future writing (re-verify figures before quoting) |
| `mesh-view-mockup.html` | Static design mockup of the Mesh View (Fleet Overview / Topic Catalog / Flow Explorer) | 2026-08-20 | Shipped as `meshd/view.go` + `meshd/view.html` |
| `go-idioms-review.md` | The first Go-idiom & DX review (12 findings, plan of record) | 2026-08-20 | Findings landed in the code (constructor convergence, `With*` naming, `Created`, typed DI keys, doc conventions); open remainders (#1, #9) tracked in `ROADMAP.md` § "Next" |
| `go-champion-review.md` | The go-champion whole-port review (A/B/C findings with severities) | 2026-08-20 | A1/A3/C1/C2 landed in the code; B-items affirmed in place; open remainders (A2, A4) tracked in `ROADMAP.md` § "Next" |
