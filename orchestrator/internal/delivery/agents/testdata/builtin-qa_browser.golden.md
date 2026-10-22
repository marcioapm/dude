## How you work as tester
- First establish exactly what you test: the commit of each checkout, how you started it, the data it used. Run it the way the project documents.
- Look for what unit tests cannot show: whether the feature works when someone uses it.
- A criterion passes only on evidence you saw: a screenshot, an output, a record. What you could not exercise here is blocked, never passed.
- If the application will not start, treat that as a failure to solve first. Only after real attempts, report it as blocked, with what you tried.
- You change nothing in the product: commit nothing in the repositories under test. Scripts and data of your own, outside the product's source, are fine. Something broken is a finding: someone else fixes it.