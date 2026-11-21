// Package selinux holds the iormesh-selinux policy module and its RPM spec
// (PLAN.md #13, task 7h4). These tests read the sources as text, so CI
// needs no SELinux tools: they pin the module's exact permissions and its
// packaging, and refuse wildcards, complements and permissive domains.
package selinux

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// wantAllows are the module's only allow rules: the tracer's seven
// capabilities (#13's set, granted here so the type does not depend on
// virt_sandbox_use_all_caps, task gh4), task zg4's measured permissions,
// which el9 showed container_t lacks, and the Java attach to the app
// containers' JVMs (container_t), which task 7h4's audit2allow review of
// a permissive el9 run found - nothing else.
var wantAllows = []string{
	"allow iormesh_container_t self:capability { dac_override kill sys_ptrace sys_resource };",
	"allow iormesh_container_t self:capability2 { bpf perfmon checkpoint_restore };",
	"allow iormesh_container_t self:bpf { map_create map_read map_write prog_load prog_run };",
	"allow iormesh_container_t self:perf_event { open cpu kernel read write tracepoint };",
	"allow iormesh_container_t tracefs_t:dir { search read open getattr };",
	"allow iormesh_container_t tracefs_t:file { open read getattr };",
	"allow iormesh_container_t container_t:dir { search getattr };",
	"allow iormesh_container_t container_t:file { open read getattr };",
	"allow iormesh_container_t container_t:lnk_file read;",
	"allow iormesh_container_t container_t:process signal;",
	"allow iormesh_container_t container_t:unix_stream_socket connectto;",
}

// wantAttrs are the typeattribute statements: container_t's own attributes
// beside container-selinux's template.
var wantAttrs = []string{
	"typeattribute iormesh_container_t container_net_domain, container_user_domain;",
	"typeattribute iormesh_container_t sandbox_net_domain, sandbox_caps_domain;",
	"typeattribute iormesh_container_t svirt_sandbox_domain;",
}

// policy returns iormesh.te's statements: comments and blank lines
// dropped, each line trimmed.
func policy(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("iormesh.te")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if i := strings.Index(l, "#"); i >= 0 {
			l = l[:i]
		}
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// TestPolicyRules: exactly the eleven allow rules and the three attribute
// lines, the module named iormesh, the type from container-selinux's
// template with container_file_t.
func TestPolicyRules(t *testing.T) {
	var allows, attrs []string
	lines := policy(t)
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "allow "):
			allows = append(allows, l)
		case strings.HasPrefix(l, "typeattribute "):
			attrs = append(attrs, l)
		}
	}
	if !slices.Equal(allows, wantAllows) {
		t.Errorf("allow rules:\n%s\nwant:\n%s", strings.Join(allows, "\n"), strings.Join(wantAllows, "\n"))
	}
	if !slices.Equal(attrs, wantAttrs) {
		t.Errorf("typeattribute lines:\n%s\nwant:\n%s", strings.Join(attrs, "\n"), strings.Join(wantAttrs, "\n"))
	}
	for _, want := range []string{"policy_module(iormesh, 0.1.0)",
		"container_domain_template(iormesh_container, container)"} {
		if !slices.Contains(lines, want) {
			t.Errorf("iormesh.te lacks %q", want)
		}
	}
}

// TestPolicyNothingBroad refuses anything wider than named permissions:
// a wildcard or complement, a permissive domain, an unconfined or
// MCS-exempting interface or attribute, a boolean or optional block,
// and any statement kind other than the declarations, the template call
// and the allow and typeattribute lines TestPolicyRules pins.
func TestPolicyNothingBroad(t *testing.T) {
	broad := regexp.MustCompile(`\*|~|\bpermissive\b|unconfined|mcs_file_read_all|mcs_file_write_all|` +
		`mcsreadall|mcswriteall|mls_|mcs_ptrace_all|domain_obj_id_change_exemption|files_read_all|` +
		`kernel_read_all|corenet_unconfined|tunable_policy|optional_policy|allowxperm|type_transition|` +
		`dontaudit|neverallow|typebounds|role_transition|range_transition`)
	known := regexp.MustCompile(`^(policy_module|gen_require|container_domain_template|allow |typeattribute |` +
		`attribute |type |class |'\))`)
	for _, l := range policy(t) {
		if broad.MatchString(l) {
			t.Errorf("broad statement in iormesh.te: %s", l)
		}
		if !known.MatchString(l) {
			t.Errorf("unexpected statement in iormesh.te: %s", l)
		}
	}
}

// TestSpec: the RPM builds the module with selinux-policy-devel against
// container-selinux, installs it in %post with semodule -i at priority 200
// (400 stays the node owner's), removes it in %postun on erase only (not
// on upgrade), and requires container-selinux.
func TestSpec(t *testing.T) {
	b, err := os.ReadFile("iormesh-selinux.spec")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(b)
	for _, want := range []string{
		"BuildRequires:  selinux-policy-devel",
		"BuildRequires:  container-selinux",
		"Requires:       container-selinux",
		"Source0:        %{modulename}.te",
		"make -f %{_datadir}/selinux/devel/Makefile %{modulename}.pp",
		"%post\n/usr/sbin/semodule -s %{selinuxtype} -X 200 -i \\\n" +
			"    %{_datadir}/selinux/packages/%{selinuxtype}/%{modulename}.pp.bz2\n",
		"if [ \"$1\" -eq 0 ]; then\n    /usr/sbin/semodule -s %{selinuxtype} -X 200 -r %{modulename}\nfi",
		"%global modulename iormesh\n",
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("spec lacks %q", want)
		}
	}
}
