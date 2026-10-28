# Owner-note round 3 plan (base f537ec6f7)

1. decide: clear st.PersonSaid in `stop` and `done`. Test: waiting/stop/done routes assert PersonSaid == ""; stop-then-resume scenario: next phase Run's person_note is '' (red at base).
2. awaitRead clears st.PersonSaid (like waitForPR). Test: decide wait -> BeforePR integration -> no PR -> awaitRead -> PersonSaid "" (red first).
3. Table test: implementRun/simplify/test/waitForPR each driven once with a note; created Run's person_note == note; following phase gets ''. Kill M1, M2, M3, M7.
4. Zero-review path keeps note; next phase Run gets it. Kill M8.
5. Rename assertOwnerRunInput -> assertOwnerNoteOnRun comparing note directly; length check first.
6. git rm .taskdocs/owner-fix-plan.md .taskdocs/owner-fix-report.md.
Gates: delivery/phases/api tests on real PG; phases 4 base failures same; build, vet, bun typecheck, diff --check, test-id diff.
Report: /workspace/out/owner-r3-report.md.
