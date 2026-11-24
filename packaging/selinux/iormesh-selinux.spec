# The iormesh_container_t SELinux policy module (docs/decisions.md #13, task
# 7h4): an opt-in type for the in-pod tracer on SELinux-enforcing el9 nodes;
# spc_t stays the default. Built by `mage selinuxRPM` (build.sh) on Rocky
# Linux 9 against its selinux-policy-devel and container-selinux.
%global modulename iormesh
%global selinuxtype targeted

Name:           iormesh-selinux
Version:        0.1.0
Release:        1%{?dist}
Summary:        SELinux type iormesh_container_t for the iormesh in-pod tracer
# The project has not chosen a license yet.
License:        LicenseRef-TBD
BuildArch:      noarch
Source0:        %{modulename}.te

BuildRequires:  make
BuildRequires:  selinux-policy-devel
# container.if (container_domain_template) and the container attributes.
BuildRequires:  container-selinux
# The type derives from container-selinux's container domain: without
# that module loaded this one does not link.
Requires:       container-selinux
Requires:       selinux-policy-%{selinuxtype}
Requires(post): policycoreutils
Requires(postun): policycoreutils

%description
A container domain built like container_t (container-selinux's template,
MCS-constrained) that may also load and run BPF programs, open the perf
events that attach tracepoints, read the host's tracefs, and attach to
the JVMs of the app containers (container_t) at its own MCS level - what
the iormesh tracer needs and container_t is denied. Deploy the tracer with
`iormeshctl deploy -selinux-type iormesh_container_t -selinux-level
s0:cN,cM` (the pod's own MCS pair).

%prep
cp %{SOURCE0} .

%build
make -f %{_datadir}/selinux/devel/Makefile %{modulename}.pp
bzip2 -9 %{modulename}.pp

%install
install -D -m 0644 %{modulename}.pp.bz2 \
    %{buildroot}%{_datadir}/selinux/packages/%{selinuxtype}/%{modulename}.pp.bz2

# Priority 200, the distribution packages' (container, k3s), so 400
# stays free for the node owner's local overrides.
%post
/usr/sbin/semodule -s %{selinuxtype} -X 200 -i \
    %{_datadir}/selinux/packages/%{selinuxtype}/%{modulename}.pp.bz2

%postun
# $1 is 0 on erase, 1 on upgrade (the new package's %post reinstalled it).
# Stop every iormesh_container_t tracer first: once the type is gone its
# processes become unlabeled_t.
if [ "$1" -eq 0 ]; then
    /usr/sbin/semodule -s %{selinuxtype} -X 200 -r %{modulename}
fi

%files
%{_datadir}/selinux/packages/%{selinuxtype}/%{modulename}.pp.bz2

%changelog
* Sat Nov 21 2026 iormesh <iormesh@localhost> - 0.1.0-1
- iormesh_container_t: container_t's template plus bpf, perf_event,
  tracefs read and the Java attach to container_t (task 7h4)
