# Built by `make rpm` from a source tarball that already contains the embedded
# web UI; the defines below come from the Makefile.
%{!?package_version:%global package_version 0.0.0}
%{!?build_version:%global build_version %{package_version}}
%{!?build_commit:%global build_commit unknown}
%global debug_package %{nil}
%global _build_id_links none

Name:           homeshell
Version:        %{package_version}
Release:        1%{?dist}
Summary:        Control plane for AI coding agents
License:        MIT AND Apache-2.0
URL:            https://github.com/eclipse-enclave/homeshell
Source0:        %{name}-%{version}.tar.gz

BuildRequires:  golang
BuildRequires:  gcc-c++
BuildRequires:  webkit2gtk4.1-devel
# The desktop app's libwebkit2gtk-4.1 dependency is generated from its ELF
# sonames; named here as well so the requirement is visible in the spec.
Requires:       webkit2gtk4.1

%description
HomeShell watches and drives AI coding agents across your projects. A local
daemon tracks every agent session, whether it runs on your machine or in an
Eclipse Enclave container, and shows them all in one web UI or desktop window.

This package contains the homeshell daemon and the homeshell-desktop app.

%prep
%setup -q

%build
test -f daemon/internal/webui/dist/index.html || { echo "the web UI is not embedded; build with make rpm" >&2; exit 1; }
export GOCACHE="%{_builddir}/%{name}-%{version}/.gocache"
export GOMODCACHE="%{_builddir}/%{name}-%{version}/.gomodcache"
export GOFLAGS="-modcacherw -trimpath"
mkdir -p bin
(cd daemon && CGO_ENABLED=0 go build \
    -ldflags "-X main.version=%{build_version} -X main.commit=%{build_commit}" \
    -o ../bin/homeshell ./cmd/homeshell)
# webview_go asks pkg-config for webkit2gtk-4.0; the shim answers from 4.1.
# Only an absolute path works (docs/DEV.md, "Desktop shell").
(cd desktop && PKG_CONFIG_PATH="$PWD/../packaging/linux/pkgconfig" CGO_ENABLED=1 go build \
    -ldflags "-X main.version=%{build_version}" \
    -o ../bin/homeshell-desktop ./cmd/homeshell-desktop)
ldd bin/homeshell-desktop | grep -q 'libwebkit2gtk-4\.1' || { echo "homeshell-desktop does not link libwebkit2gtk-4.1" >&2; exit 1; }

%install
doc_root="%{buildroot}%{_datadir}/doc/%{name}"
install -D -m 0755 bin/homeshell "%{buildroot}%{_bindir}/homeshell"
install -D -m 0755 bin/homeshell-desktop "%{buildroot}%{_bindir}/homeshell-desktop"
install -D -m 0644 packaging/linux/homeshell.desktop "%{buildroot}%{_datadir}/applications/homeshell.desktop"
install -D -m 0644 packaging/linux/homeshell.png "%{buildroot}%{_datadir}/icons/hicolor/512x512/apps/homeshell.png"
install -D -m 0644 README.md "$doc_root/README.md"
install -D -m 0644 LICENSE "$doc_root/LICENSE"
install -D -m 0644 NOTICE.md "$doc_root/NOTICE.md"
# The daemon embeds herdr's detection manifests; Apache-2.0 requires its license.
install -D -m 0644 packaging/THIRD-PARTY-NOTICES.txt "$doc_root/THIRD-PARTY-NOTICES.txt"
install -D -m 0644 daemon/internal/detect/manifests/herdr/LICENSE "$doc_root/licenses/herdr/LICENSE"

%clean
for cache in .gocache .gomodcache; do
    if [ -d "$cache" ]; then
        chmod -R u+w "$cache"
    fi
done
rm -rf "%{buildroot}"

%files
%{_bindir}/homeshell
%{_bindir}/homeshell-desktop
%{_datadir}/applications/homeshell.desktop
%{_datadir}/icons/hicolor/512x512/apps/homeshell.png
%dir %{_datadir}/doc/%{name}
%doc %{_datadir}/doc/%{name}/README.md
%license %{_datadir}/doc/%{name}/LICENSE
%license %{_datadir}/doc/%{name}/NOTICE.md
%license %{_datadir}/doc/%{name}/THIRD-PARTY-NOTICES.txt
%license %{_datadir}/doc/%{name}/licenses

%changelog
* Fri Nov 06 2026 HomeShell contributors <homeshell@eclipse.org> - %{version}-%{release}
- Initial package.
