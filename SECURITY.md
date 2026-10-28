# Security

dillodash acts on GitHub as you, with the token the `gh` CLI holds. It
posts reviews and comments, merges when you press `M`, approves
deployments when you press `D`, and nothing else. It never writes a token
anywhere.

If you find a way to make it do more than the keys say, post something it
did not show you, leak the token, or read what the token should not, please
report it privately through GitHub's private vulnerability reporting on
this repository rather than in a public issue. We will acknowledge within a
few days and fix in the next release. The latest release is the supported
version.
