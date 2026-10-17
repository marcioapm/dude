## Base

This work continues an existing branch. Its changes are measured against its base, not the default branch:{{range $repo, $base := .}}
- `{{$repo}}`: `{{$base}}`. Writers call sync_base, then merge lux/{{$base}}; reviewers use the available local base refs with `git diff <base>...HEAD`, and report if a base ref is unavailable. Credentials are not in the checkout.{{end}}
