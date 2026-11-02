# Security

dillodash acts on GitHub as you, with the token the `gh` CLI holds. It
posts reviews and comments, merges when you press `M`, approves
deployments when you press `D`, and nothing else. It never writes a token
anywhere, and it refuses to run signed in as a bot account.

If you find a way to make it do more than the keys say, post something it
did not show you, leak the token, or read what the token should not, please
report it privately through [GitHub's private vulnerability
reporting](https://github.com/solcyber/gh-dillodash/security/advisories/new)
on this repository rather than in a public issue. We will acknowledge within
a few days and fix in the next release.

## Supported versions

The latest release is the supported version. dillodash moves you to it by
itself (`[update] auto`), and `gh extension upgrade dillodash` or
`gh dillodash update` does it by hand.

## How an update is verified

dillodash installs only a published, non-draft, non-prerelease release
of `solcyber/gh-dillodash` that is newer than the running build; it
never downgrades. Before a downloaded binary replaces anything, both
checks must pass:

1. its SHA-256 equals the `digest` GitHub reports for that release asset;
2. `gh attestation verify <file> --repo solcyber/gh-dillodash` passes:
   the binary carries a build-provenance attestation from this
   repository's release workflow.

Either one failing (or `gh` missing) refuses the install, deletes the
download, and leaves every binary on disk as it was; the status line
says so and names the command that upgrades by hand. The binary is
written beside each install and renamed over it, the old one kept as
`<name>.prev` (`gh dillodash update --rollback` puts it back). A
launcher script is never replaced, nothing runs with elevated
privileges, and an install path dillodash cannot write is left alone
with the one command that would update it.
