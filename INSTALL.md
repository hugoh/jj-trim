<!-- DO NOT EDIT — managed by hugoh/go-tools via copier. -->

# Installing jj-trim

## Homebrew (macOS/Linux)

```sh
brew install hugoh/tap/jj-trim
```

## Debian/Ubuntu (apt)

```sh
curl -1sLf 'https://dl.cloudsmith.io/public/hugoh/apt/setup.deb.sh' | sudo -E bash
sudo apt install jj-trim
```

## RHEL/Fedora (dnf)

```sh
curl -1sLf 'https://dl.cloudsmith.io/public/hugoh/rpm/setup.rpm.sh' | sudo -E bash
sudo dnf install jj-trim
```

Package repository hosting is graciously provided by
[Cloudsmith](https://cloudsmith.com).

## Package files

Download the `.deb` or `.rpm` from the
[releases page](https://github.com/hugoh/jj-trim/releases) and install it
with your package manager:

```sh
# Debian/Ubuntu
sudo apt install ./jj-trim_*.deb

# RHEL/Fedora
sudo dnf install ./jj-trim_*.rpm
```

## mise

```sh
mise use -g github:hugoh/jj-trim
```
