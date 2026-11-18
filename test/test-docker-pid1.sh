#!/bin/sh
# Run a covered test binary as PID 1, without --init or a shell entrypoint.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
IMAGE=${XEXEC_DOCKER_IMAGE:-golang:latest}
WORK=$(mktemp -d)
cleanup() { rm -rf -- "$WORK"; }
trap cleanup EXIT HUP INT TERM
mkdir "$WORK/coverage"

# Keep build outputs outside the checkout. The race binary uses glibc from the
# local Go image. --pull=never makes missing images fail rather than pull silently.
(cd "$ROOT" && GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go test -c -race -cover -o "$WORK/xexec.test" .)
docker run --rm --pull=never --network=none --read-only --tmpfs /tmp:rw,exec,nosuid,size=128m \
    --mount "type=bind,src=$WORK,dst=/tests,readonly" \
    --mount "type=bind,src=$WORK/coverage,dst=/coverage" \
    --env XEXEC_DOCKER_PID1=1 \
    "$IMAGE" /tests/xexec.test \
    -test.run '^TestDockerPID1$' -test.count "${XEXEC_DOCKER_TEST_COUNT:-1}" -test.v -test.timeout 60s \
    -test.coverprofile=/coverage/coverage.out

go tool cover -func="$WORK/coverage/coverage.out"
if [ -n "${XEXEC_DOCKER_COVERPROFILE:-}" ]; then
    cp "$WORK/coverage/coverage.out" "$XEXEC_DOCKER_COVERPROFILE"
fi
