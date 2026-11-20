# The tracer's runtime image for Kubernetes (PLAN.md M7, decision #13;
# docs/implementation.md M7 step 8): the static tracer and the Java agent
# jar, nothing else - no shell, no package manager, no libc. It is not a
# builder: the binaries come from the release pipeline (`mage buildPodman`
# or `mage buildDocker` for dist/iormesh-linux-<arch>, `mage agentBuild`
# for iormesh-agent.jar), so the image carries the very bytes that were
# verified and smoke-run there.
#
# Build from the repository root, one image per arch, then push and pin
# the digest (k8s.DefaultImage, deploy/):
#   podman build --platform linux/amd64 \
#     -f scripts/container/tracer-image.Containerfile -t <registry>/iormesh .
# TARGETARCH comes from --platform (BuildKit and Buildah set it); a builder
# without that takes --build-arg TARGETARCH=amd64.

# scratch: the tracer links fully static (docs/build.md) and reads only
# the kernel's files (/proc, BTF in /sys, tracefs mounted by the pod), so
# a base image would add nothing but attack surface and a digest to track.
FROM scratch

ARG TARGETARCH

# Paths match a server install (internal/cli): the binary where the
# sudoers rule names it, traces under the default -trace-dir (the pod's
# emptyDir mounts there). Read-only modes: nothing in the image is ever
# written, and the container runs as root.
COPY --chmod=0555 dist/iormesh-linux-${TARGETARCH} /opt/iormesh/bin/iormesh
COPY --chmod=0444 iormesh-agent.jar /opt/iormesh/lib/iormesh-agent.jar

# Root is required (decision #13: BPF and the HotSpot attach accept uid 0);
# the pod's securityContext drops every capability but #13's set. Said
# explicitly so the image's user is a reviewed choice, not a default.
# hadolint ignore=DL3002
USER 0:0

# The controller execs the tracer by name (`kubectl exec … -- iormesh …`,
# k8s.TracerBinary), and scratch has no PATH of its own.
ENV PATH=/opt/iormesh/bin

ENTRYPOINT ["/opt/iormesh/bin/iormesh"]
CMD ["version"]
