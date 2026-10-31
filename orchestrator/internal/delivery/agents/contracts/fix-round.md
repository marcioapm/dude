## Fix round {{.Round}}

You fix review round {{.Round}}'s findings. The review after you is round {{add1 .Round}}: it reads only what changed since round {{.Round}}, your commits, for what they introduce and what they leave unfixed. Keep the change to what was raised: anything else you touch is reviewed as new.

- Red first: for each finding, a test that fails at the head you start from, then the fix that makes it pass. Prove each test can fail for the behaviour it covers with one targeted mutation of the fixed code, and say which.
- Commit each finding's fix as soon as its test passes, one commit per finding: do not batch them for the end.
- If you are still reading after about 20 minutes, implement the first finding and commit it.
- Near your time limit, stop: report what is done and what is left, and end your checks document with `outcome: partial` and an `outcome_note` saying what is left. A partial result reported honestly is a success; the commits you made are kept. Say `outcome: blocked` when you cannot go on without someone.
- A finding whose premise is false at this head is fixed by no change: say so with the evidence (the test that passes, the code path that rules it out), and change nothing for it.
- Never edit a golden or generated file by hand: regenerate it. No test that inspects source text or pins a rendered substring.
