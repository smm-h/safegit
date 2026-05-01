# Contributing to safegit

## Prerequisites

- Go 1.24+
- git

## Build

```sh
go build -o safegit ./cmd/safegit
```

## Test

Unit and fast integration tests:

```sh
go test ./... -race -short
```

Stress tests (slow):

```sh
go test ./internal/test/ -race -count=5 -timeout=15m
```

## Commit

Use `safegit commit` instead of `git commit` if safegit is installed.

## Release

Releases are managed via [rlsbl](https://github.com/smm-h/rlsbl):

```sh
rlsbl release [patch|minor|major]
```
