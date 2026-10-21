## The last fix

This round follows a fix, whose commits are {{range $i, $r := .}}{{if $i}}; {{end}}in `{{$r.Repo}}`, `git log {{$r.Base}}..{{$r.Head}}`{{end}}. Read them to answer each new finding's `caused_by_fix`: `yes` when the problem is in code those commits wrote, `no` otherwise.
