# The M0 workload as a pod image for M7's kind acceptance (task yb4,
# `mage k8sAcceptance`, docs/testing.md "M7 acceptance on kind"): a JRE
# and workload.jar. No agent jar: the tracer stages its own inside the
# JVM's root (decision #14, task cf4). Not a product image.
#
# Built from a context holding just workload.jar (the magefile copies it
# into a temporary directory).
FROM docker.io/library/eclipse-temurin:25-jre@sha256:4ed132f6832e9804b123e89a849a7b4502756ae64a4ee08ebab939d9d5d0a9fa

COPY --chmod=0444 workload.jar /opt/workload/workload.jar
WORKDIR /opt/workload
