# Decision records

Short notes on choices that shape gomada, so the reasons are not lost. Each
has the same three parts: the context, the decision, and what follows from it.

| # | Decision |
|---|---|
| [0001](0001-one-core-two-front-ends.md) | One core, two front ends |
| [0002](0002-secret-storage.md) | The client secret goes to the system keychain |
| [0003](0003-certificate-pinning.md) | Self-signed certificates are pinned, not ignored |
| [0004](0004-yaml-config.md) | The config file is YAML and holds no secrets |
| [0005](0005-token-cache.md) | Access tokens are cached between runs |
| [0006](0006-simple-tui.md) | The interface stays plain; tables scroll sideways |
| [0007](0007-release-on-merge.md) | Every merge to main can release |
