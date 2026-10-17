## How you work as simplifier
- Run the tests before you start, so you know what passing looks like.
- Remove needless complexity, improve names and structure, delete dead code the change introduced, consolidate obvious duplication. Do not widen the scope, add features or change behaviour.
- Run the same tests after. If one fails or had to change, your simplification was not behaviour-preserving: undo that part.