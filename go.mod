module github.com/TAIPANBOX/mockryx

go 1.26

toolchain go1.26.5

require (
	github.com/TAIPANBOX/agent-stack-go v0.0.0
	gopkg.in/yaml.v3 v3.0.1
)

// agent-stack-go is not published yet: it lives locally, as a sibling
// checkout of this repo. Remove this replace once TAIPANBOX/agent-stack-go
// has a tagged release and pin a real version instead.
replace github.com/TAIPANBOX/agent-stack-go => ../agent-stack-go
