module github.com/eclipsesource/homeshell/daemon

go 1.25.7

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/creack/pty v1.1.24
	github.com/hinshun/vt10x v0.0.0-20220301184237-5011da428d02
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	golang.org/x/term v0.43.0
	golang.org/x/text v0.37.0
)

require golang.org/x/sys v0.45.0 // indirect

// The gravitational fork is Teleport's maintained vt10x: fuzz-hardened CSI
// parsing, same module path. Evaluated against a tmux-fidelity harness during
// prototyping (20/20 byte-identical, 20/20 detection parity).
replace github.com/hinshun/vt10x => github.com/gravitational/vt10x v0.0.6-teleport.1
