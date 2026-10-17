FROM alpine:3.21.3
ARG KIND_VERSION=v0.27.0
ARG KUBECTL_VERSION=v1.32.2
RUN apk add --no-cache bash ca-certificates curl docker-cli && \
    curl -fsSL "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-amd64" -o /usr/local/bin/kind && \
    chmod +x /usr/local/bin/kind && \
    curl -fsSLo /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" && \
    chmod +x /usr/local/bin/kubectl
COPY deploy/kind/bootstrap.sh /usr/local/bin/bootstrap
RUN chmod +x /usr/local/bin/bootstrap
ENTRYPOINT ["/usr/local/bin/bootstrap"]
