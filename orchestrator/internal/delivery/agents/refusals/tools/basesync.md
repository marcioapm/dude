{{define "refusals/tools/basesync/your-container-not-running-as-lux"}}your container is not running as lux sees it; call sync_base again in a moment{{end}}

{{- define "refusals/tools/basesync/this-run-holds-no-repository-may"}}this Run holds no repository it may change: there is no base to bring in{{end}}

{{- define "refusals/tools/basesync/this-dude-cannot-ask-lux-for"}}this dude cannot ask lux for a sync: sync_base is not available here{{end}}

{{- define "refusals/tools/basesync/this-lux-has-no-sync-modes"}}this lux has no sync modes, so it cannot fetch your base without moving your checkout: sync_base needs a lux with mode fetch. Ask a person to bring the base in, or carry on without it{{end}}

{{- define "refusals/tools/basesync/lux-would-not-fetch-your-base"}}lux would not fetch your base: %s{{end}}
