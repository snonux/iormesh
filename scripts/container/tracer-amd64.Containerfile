# Adapted from ior Dockerfile @ b21afc50328c.
# Changes: base and Go pinned by digest (Go copied from the golang image
# instead of a go.dev download), zstd checksummed, the elfutils source RPM
# signature-checked (rpm -K), libbpfgo pinned by commit,
# no mage install (the build `go run`s a pinned mage), make -j4, the
# installed clang checked against CLANG_VERSION (task ll4), no
# CMD (mage drives the build with `<engine> run`, see
# internal/containerbuild).
#
# Builder for the linux/amd64 tracer (docs/build.md "Container builds"): Rocky
# Linux 9, the first tracer target, with the toolchain ior builds on. The
# image holds only the toolchain; the source tree is mounted at run time,
# so one image serves every commit.

# Go comes from the official image so its version is pinned by the same
# digest mechanism as the base (Rocky 9 ships an older Go).
FROM docker.io/library/golang:1.26.10-trixie@sha256:fd9a91f0ece5664556f4851d06dce5e5b505a71166dffd385a03ba9d200b6983 AS go

FROM docker.io/rockylinux/rockylinux:9-minimal@sha256:96aef88435673089451d3ca68780d3f68936f19a03b15b685bff54e55b6ef3a5

# The minimal image ships only microdnf; builddep and config-manager need
# full dnf.
RUN microdnf install -y dnf dnf-plugins-core && microdnf clean all

# CRB ships zlib-static and glibc-static, EPEL the rest; the BaseOS source
# repo has the elfutils source RPM libelf.a is built from.
RUN dnf config-manager --set-enabled crb && \
    dnf config-manager --set-enabled baseos-source && \
    dnf install -y epel-release && \
    dnf clean all

# clang compiles the CO-RE object, gcc links the static cgo binary, bpftool
# dumps vmlinux.h from the mounted host BTF when the tree has none.
RUN dnf install -y \
        gcc clang bpftool pkgconf-pkg-config \
        elfutils-libelf-devel zlib-static glibc-static libzstd-devel \
        git make tar bzip2 wget && \
    dnf builddep -y elfutils && \
    dnf clean all

# The BPF bytecode depends on the compiler (task il4, decision #31), and
# Rocky updates clang with its point releases (9.9 ships 22.1.8).
# CLANG_VERSION is the clang whose object was last loaded on el9 (docs/
# testing.md, "Builder clang pin"). The image build fails when the distro's
# clang has moved on, and TestBuilderClangPinned (internal/buildgate) fails
# when this value changes without the recorded one.
ARG CLANG_VERSION=22.1.8
RUN clang --version | head -n 1 | grep -F "clang version ${CLANG_VERSION} "

# Rocky 9 ships no libelf.a; build it from the distro's own source RPM so
# it matches the headers installed above. rpm -K checks the source RPM's
# digests and Rocky's signature before it is unpacked.
RUN mkdir -p /root/src && cd /root && \
    dnf download --source elfutils-libelf && \
    rpm -K elfutils-*.src.rpm && \
    rpm -ivh elfutils-*.src.rpm && \
    tar -C /root/src -xjf rpmbuild/SOURCES/elfutils-*.tar.bz2 && \
    cd /root/src/elfutils-* && \
    ./configure --enable-deterministic-archives --disable-debuginfod \
        --disable-libdebuginfod && \
    make -C lib -j4 && make -C libelf -j4 && \
    cp -v libelf/libelf.a /usr/lib64/ && \
    rm -rf /root/src /root/rpmbuild /root/elfutils-*.src.rpm

# libzstd-devel has no static archive either.
ARG ZSTD_VERSION=1.5.5
ARG ZSTD_SHA256=9c4396cc829cfae319a6e2615202e82aad41372073482fce286fac78646d3ee4
RUN wget -q "https://github.com/facebook/zstd/releases/download/v${ZSTD_VERSION}/zstd-${ZSTD_VERSION}.tar.gz" \
        -O /tmp/zstd.tar.gz && \
    echo "${ZSTD_SHA256}  /tmp/zstd.tar.gz" | sha256sum -c - && \
    tar -C /tmp -xzf /tmp/zstd.tar.gz && \
    make -C "/tmp/zstd-${ZSTD_VERSION}/lib" -j4 libzstd.a && \
    cp -v "/tmp/zstd-${ZSTD_VERSION}/lib/libzstd.a" /usr/lib64/ && \
    rm -rf /tmp/zstd-*

COPY --from=go /usr/local/go /usr/local/go
# GOTOOLCHAIN=local: the pinned Go builds, never one fetched on demand.
ENV PATH="/usr/local/go/bin:/root/go/bin:${PATH}" \
    GOPATH="/root/go" \
    GOTOOLCHAIN=local

# libbpfgo at the tag Magefile.go pins (libbpfgoRequiredTag), checked
# against the tag's commit so a moved tag fails the build.
ARG LIBBPFGO_TAG=v0.9.2-libbpf-1.5.1
ARG LIBBPFGO_COMMIT=37346eb620f2440e398a9b385e9a0143fdbee487
RUN git clone --branch "${LIBBPFGO_TAG}" https://github.com/aquasecurity/libbpfgo /git/libbpfgo && \
    test "$(git -C /git/libbpfgo rev-parse HEAD)" = "${LIBBPFGO_COMMIT}" && \
    git -C /git/libbpfgo submodule update --init --recursive && \
    make -C /git/libbpfgo libbpfgo-static
ENV LIBBPFGO=/git/libbpfgo
