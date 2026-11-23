# 0003. Self-signed certificates are pinned, not ignored

## Context

Most Omada controllers on a home or office network use a self-signed
certificate, which normal verification rejects. The common workaround, turning
verification off, lets anyone on the network path read the client secret.

## Decision

When a host is added and its certificate is not trusted by the system,
gomada shows the certificate's SHA-256 fingerprint and asks. On approval the
fingerprint is saved with the host (`tls_pin`), and from then on only that
exact certificate is accepted. A different certificate is refused with a
message that says the certificate changed.

Certificates the system already trusts are verified normally and not pinned.
Turning verification off entirely is possible only with the explicitly named
flag `--insecure-skip-tls-verify`, which prints a warning.

## Consequences

- A self-signed controller is as safe as SSH's trust-on-first-use.
- Replacing the controller's certificate means removing and re-adding the
  host.
