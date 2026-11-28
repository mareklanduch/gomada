# 0004. The config file is YAML and holds no secrets

## Context

The hosts need to be stored somewhere a person can read and, if need be, edit.

## Decision

One YAML file, `config.yaml`, in the standard configuration directory of the
operating system (`$XDG_CONFIG_HOME` is honored everywhere). YAML because it
is a common format among command-line tools. The file is read into
a typed struct directly, without a configuration framework.

- It carries a `version` number; `config.migrate` is where a future format
  change adds its step. A file from a newer gomada is refused with a clear
  message.
- Unknown fields are an error, so a typo does not silently do nothing.
- It is written atomically (temporary file, then rename) with owner-only
  permissions.

## Consequences

- No secrets or tokens are ever in the file (see 0002 and 0005).
- Adding a setting is adding a struct field.
