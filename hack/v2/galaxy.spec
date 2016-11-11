Name: galaxy
Version: %{version}
Release: %{commit}%{?dist}
Summary: kubernetes network cni plugin

License: MIT
Requires: /bin/sh
BuildRequires: pkgconfig(systemd)
Requires: systemd-units
Source: %{name}-%{version}.tar.gz

%description
kubernetes network cni plugin

%prep
%setup -q

%build
./hack/build.sh

%install
install -d $RPM_BUILD_ROOT/opt/cni/bin/
install -p -m 755 bin/galaxy-k8s-hybrid $RPM_BUILD_ROOT/opt/cni/bin/galaxy-k8s-hybrid
install -p -m 755 bin/galaxy-k8s-cni $RPM_BUILD_ROOT/opt/cni/bin/galaxy-k8s-cni
install -p -m 755 bin/galaxy-k8s-vlan $RPM_BUILD_ROOT/opt/cni/bin/galaxy-k8s-vlan
install -p -m 755 bin/galaxy-flannel $RPM_BUILD_ROOT/opt/cni/bin/galaxy-flannel
install -p -m 755 bin/galaxy-bridge $RPM_BUILD_ROOT/opt/cni/bin/galaxy-bridge
install -p -m 755 bin/loopback $RPM_BUILD_ROOT/opt/cni/bin/loopback
install -p -m 755 bin/host-local $RPM_BUILD_ROOT/opt/cni/bin/host-local

install -d $RPM_BUILD_ROOT/etc/cni/net.d/
install -p -m 644 hack/v2/galaxy.conf $RPM_BUILD_ROOT/etc/cni/net.d/galaxy.conf

%files
/opt/cni/bin/galaxy-k8s-hybrid
/opt/cni/bin/galaxy-k8s-cni
/opt/cni/bin/galaxy-k8s-vlan
/opt/cni/bin/galaxy-flannel
/opt/cni/bin/galaxy-bridge
/opt/cni/bin/loopback
/opt/cni/bin/host-local

%config(noreplace) /etc/cni/net.d/galaxy.conf

%define __debug_install_post   \
   %{_rpmconfigdir}/find-debuginfo.sh %{?_find_debuginfo_opts} "%{_builddir}/%{?buildsubdir}"\
%{nil}
