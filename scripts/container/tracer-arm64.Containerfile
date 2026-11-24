# Cross builder for the linux/arm64 tracer (docs/build.md "Container builds"):
# runs natively on an amd64 host and cross-compiles with an aarch64
# toolchain and sysroot, so no emulation is needed to build.
#
# Debian, not Rocky 9 like the amd64 builder: Rocky 9 has no aarch64
# userspace cross compiler and ships no static libelf, while Debian's
# multiarch gives both as packages (gcc-aarch64-linux-gnu plus libelf.a,
# libz.a and libzstd.a for arm64). The tracer links fully static, so the
# builder's distro and glibc do not reach the Rocky 9 hosts it runs on.
#
# The image holds only the toolchain; the source tree is mounted at run
# time (internal/containerbuild drives it), so one image serves every commit.
FROM docker.io/library/golang:1.26.10-trixie@sha256:fd9a91f0ece5664556f4851d06dce5e5b505a71166dffd385a03ba9d200b6983

# clang compiles the CO-RE object (BPF bytecode is the same for every
# host arch; only __TARGET_ARCH differs), the aarch64 gcc compiles libbpf
# and links the cgo binary, bpftool dumps vmlinux.h from the mounted host
# BTF when the tree has none.
RUN dpkg --add-architecture arm64 && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
        clang llvm bpftool make pkg-config git ca-certificates \
        gcc-aarch64-linux-gnu libc6-dev-arm64-cross \
        libelf-dev:arm64 zlib1g-dev:arm64 libzstd-dev:arm64 && \
    rm -rf /var/lib/apt/lists/*

# The BPF bytecode depends on the compiler: this clang once tested a slot
# index on a copy that the el9 verifier refused (task il4, decision #31).
# CLANG_VERSION is the clang whose object was last loaded on el9 (docs/
# testing.md, "Builder clang pin"). The image build fails when the distro's
# clang has moved on, and TestBuilderClangPinned (internal/buildgate) fails
# when this value changes without the recorded one.
ARG CLANG_VERSION=19.1.7
RUN clang --version | head -n 1 | grep -F "clang version ${CLANG_VERSION} "

# Since elfutils 0.191 libelf calls eu_search_tree_* from its internal
# libeu.a, which libelf.so links in but the static libelf.a does not, so
# a static link fails on undefined references. Compile eu-search.o from
# the same elfutils release as the packaged libelf (checked) and add it to
# libelf.a. ELFUTILS_SHA256 was computed from the release tarball after
# checking it against the release's published sha512.sum
# (https://sourceware.org/elfutils/ftp/0.192/sha512.sum, 2026-11-20).
ARG ELFUTILS_VERSION=0.192
ARG ELFUTILS_SHA256=616099beae24aba11f9b63d86ca6cc8d566d968b802391334c91df54eab416b4
RUN apt-get update && \
    apt-get install -y --no-install-recommends bzip2 gawk m4 && \
    rm -rf /var/lib/apt/lists/* && \
    dpkg-query -W -f='${Version}' libelf-dev:arm64 | grep -q "^${ELFUTILS_VERSION}-" && \
    curl -fsSL -o /tmp/elfutils.tar.bz2 \
        "https://sourceware.org/elfutils/ftp/${ELFUTILS_VERSION}/elfutils-${ELFUTILS_VERSION}.tar.bz2" && \
    echo "${ELFUTILS_SHA256}  /tmp/elfutils.tar.bz2" | sha256sum -c - && \
    tar -C /tmp -xjf /tmp/elfutils.tar.bz2 && \
    cd "/tmp/elfutils-${ELFUTILS_VERSION}" && \
    ./configure --host=aarch64-linux-gnu --disable-debuginfod \
        --disable-libdebuginfod --disable-demangler \
        --without-bzlib --without-lzma && \
    make -C lib eu-search.o && \
    aarch64-linux-gnu-ar rs /usr/lib/aarch64-linux-gnu/libelf.a lib/eu-search.o && \
    cd / && rm -rf /tmp/elfutils*

ENV GOTOOLCHAIN=local

# libbpfgo at the tag Magefile.go pins (libbpfgoRequiredTag), checked
# against the tag's commit so a moved tag fails the build. Only libbpf.a
# and its headers are built - for arm64, with the cross compiler; the
# libbpfgo Go package itself is compiled with the tracer.
ARG LIBBPFGO_TAG=v0.9.2-libbpf-1.5.1
ARG LIBBPFGO_COMMIT=37346eb620f2440e398a9b385e9a0143fdbee487
RUN git clone --branch "${LIBBPFGO_TAG}" https://github.com/aquasecurity/libbpfgo /git/libbpfgo && \
    test "$(git -C /git/libbpfgo rev-parse HEAD)" = "${LIBBPFGO_COMMIT}" && \
    git -C /git/libbpfgo submodule update --init --recursive && \
    make -C /git/libbpfgo libbpf-uapi libbpf-static CC=aarch64-linux-gnu-gcc
ENV LIBBPFGO=/git/libbpfgo
