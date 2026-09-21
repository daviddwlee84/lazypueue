#!/bin/sh
set -eu
mkdir -p completions
go run . completion bash > completions/lazypueue.bash
go run . completion zsh > completions/lazypueue.zsh
test -s completions/lazypueue.bash
test -s completions/lazypueue.zsh
