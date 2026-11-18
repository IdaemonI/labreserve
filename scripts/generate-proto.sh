#!/bin/sh
set -eu

# Install official generators into a temporary directory; the runtime build needs
# only the checked-in generated files. protoc 34.1 is the pinned compiler.
if [ "$(protoc --version)" != "libprotoc 34.1" ]; then
  printf '%s\n' 'Install protoc 34.1 from https://github.com/protocolbuffers/protobuf/releases/tag/v34.1' >&2
  exit 1
fi
tool_dir=$(mktemp -d)
trap 'rm -rf "$tool_dir"' EXIT HUP INT TERM
GOBIN="$tool_dir" go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
GOBIN="$tool_dir" go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.1
PATH="$tool_dir:$PATH" protoc --proto_path=api/proto \
  --go_out=. --go_opt=module=github.com/IdaemonI/labreserve \
  --go-grpc_out=. --go-grpc_opt=module=github.com/IdaemonI/labreserve \
  api/proto/diagnostics.proto
