# Multi-stage build for mgtt-provider-terraform.
# The provider binary shells out to `terraform`, so the runtime image must
# include it. We stage the official hashicorp/terraform CLI onto a minimal
# alpine runtime.
#
# NOTE: The container needs access to the operator's Terraform working
# directory (the `.terraform/` and state files) to be useful. When using
# `--image`, mount it — e.g. `docker run -v $(pwd):/workspace -w /workspace
# <image>`. mgtt's image runner does not yet forward mounts/env; this is a
# known limitation to be addressed in a future mgtt release.

FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/provider .

FROM alpine:3.20
ARG TERRAFORM_VERSION=1.9.8
RUN apk add --no-cache ca-certificates curl unzip \
 && curl -sSL -o /tmp/tf.zip \
      "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_amd64.zip" \
 && unzip -d /usr/local/bin/ /tmp/tf.zip \
 && rm /tmp/tf.zip \
 && apk del unzip \
 && terraform version
COPY --from=build /out/provider /usr/local/bin/provider
COPY provider.yaml /provider.yaml
COPY types /types
ENTRYPOINT ["/usr/local/bin/provider"]
