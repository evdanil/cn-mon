# cn-mon MSIX

The MSIX installs the existing Windows x64 command-line release and adds a normal Start entry alongside the `cn-mon.exe` command alias. Open PowerShell or Command Prompt and run `cn-mon --help`; use `cn-mon.exe` when a command or script needs the explicit executable suffix.

The package version is maintained independently from the CLI version. The current package version is `1.3.2.0`, containing the unchanged CLI `0.3.0` release binary. Run `cn-mon --version` to see the CLI version and source revision.

The program reads the current working directory for caller-relative paths. Its defaults and configuration precedence match the portable Windows release: user configuration is under `%USERPROFILE%\.cn`, history defaults to `%USERPROFILE%\.cn-ping-history.db`, and the default log is `%USERPROFILE%\cn.log`. Use `--config`, `--history-file`, or `--log-file` to select other paths. The package contains no user configuration or history data.

If another command named `cn-mon` appears earlier on `PATH`, it can take precedence over the package alias. Check command resolution with `Get-Command cn-mon -All` in PowerShell or `where.exe cn-mon` in Command Prompt. Windows manages packaged aliases in **Settings → Apps → Advanced app settings → App execution aliases**; use that page to enable or disable the package alias without changing `PATH` globally.

These instructions describe the package contract. They do not indicate that an MSIX has been installed or qualified on this machine.
