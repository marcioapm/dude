{{define "notices/web-changed/reach"}}{{if .Search}}web search{{else}}no web search{{end}}, {{if .Fetches}}{{if .AnyHost}}fetching from any host{{else}}fetching from {{.HostList}} only{{end}}{{else}}no fetching{{end}}{{end}}
{{- /* What a session's new agent is told when the agent before it reached the web otherwise. */ -}}
Your web access is not what this session's agent before you had. It had {{template "notices/web-changed/reach" .Before}}; you have {{template "notices/web-changed/reach" .Now}}. A search or fetch earlier in the conversation was made with its tools, not yours.
