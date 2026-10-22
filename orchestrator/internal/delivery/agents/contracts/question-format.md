Ask a question instead of guessing only when a fact you cannot establish from the code, its configuration or its history would change a severity or a verdict. Never ask what you can find out. A question is not a finding: it goes to a person, not a fixer. Ask each as one more YAML document:

```yaml
question: The narrow factual question.
why_it_matters: Which finding or verdict changes with the answer, and how.
evidence_checked: What you read and ran to try to establish it.
needed_from: Who can answer: product, operations, the data owner, security…
about: F2
```

`about` names the earlier finding the question is about, when it is about one. If you disagree with an earlier finding's `Done when`, ask a question about that finding instead of giving it a verdict: only a person changes a criterion.
