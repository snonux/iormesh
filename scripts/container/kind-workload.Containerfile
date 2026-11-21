# The M0 workload as a pod image for M7's kind acceptance (task yb4,
# `mage k8sAcceptance`, docs/testing.md "M7 acceptance on kind"): a JRE,
# workload.jar, and — test-only — the Java agent jar at the tracer
# image's own path. The agent copy is the workaround for the unbuilt M7
# step 3 (decision #14, docs/implementation.md): until the tracer stages
# the jar and spool inside the JVM's root, a JVM in another container
# must see both at the tracer's paths. Not a product image.
#
# Built from a context holding just workload.jar and iormesh-agent.jar
# (the magefile copies them into a temporary directory).
FROM docker.io/library/eclipse-temurin:25-jre@sha256:4ed132f6832e9804b123e89a849a7b4502756ae64a4ee08ebab939d9d5d0a9fa

COPY --chmod=0444 workload.jar /opt/workload/workload.jar
COPY --chmod=0444 iormesh-agent.jar /opt/iormesh/lib/iormesh-agent.jar
WORKDIR /opt/workload
