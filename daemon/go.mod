module github.com/eclipsesource/homeshell/daemon

go 1.25.7

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/Masterminds/semver/v3 v3.4.0
	github.com/creack/pty v1.1.24
	github.com/creativeprojects/go-selfupdate v1.5.2
	github.com/hinshun/vt10x v0.0.0-20220301184237-5011da428d02
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	golang.org/x/term v0.43.0
	golang.org/x/text v0.37.0
)

require (
	code.gitea.io/sdk/gitea v0.22.1 // indirect
	github.com/42wim/httpsig v1.2.3 // indirect
	github.com/davidmz/go-pageant v1.0.2 // indirect
	github.com/go-fed/httpsig v1.1.0 // indirect
	github.com/google/go-github/v74 v74.0.0 // indirect
	github.com/google/go-querystring v1.1.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-retryablehttp v0.7.8 // indirect
	github.com/hashicorp/go-version v1.8.0 // indirect
	github.com/mattn/go-isatty v0.0.21 // indirect
	github.com/ulikunitz/xz v0.5.15 // indirect
	gitlab.com/gitlab-org/api/client-go v1.9.1 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/oauth2 v0.34.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// The gravitational fork is Teleport's maintained vt10x: fuzz-hardened CSI
// parsing, same module path. Evaluated against a tmux-fidelity harness during
// prototyping (20/20 byte-identical, 20/20 detection parity).
replace github.com/hinshun/vt10x => github.com/gravitational/vt10x v0.0.6-teleport.1
