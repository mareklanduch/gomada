# Everyday commands. Run `make check` before opening a pull request; it is
# what CI runs.

.PHONY: build test lint fmt golden check

build:
	cargo build --release

test:
	cargo test
	bash scripts/next-version_test.sh

lint:
	cargo fmt --check
	cargo clippy --all-targets -- -D warnings

fmt:
	cargo fmt

# Rewrite the golden files after an intended change to output or screens.
# Review the diff like code.
golden:
	UPDATE_GOLDEN=1 cargo test

check: lint test
