# Compatible password hashing

This directory contains only the BSD-licensed Go Authors' bcrypt implementation
and its Blowfish primitive, copied from the exact release in `upstream.json`.
OpenPGP and the umbrella `golang.org/x/crypto` module are absent from the
application dependency graph. Algorithms, formats and error behavior are unchanged;
the only source modification redirects bcrypt's Blowfish import here.

The upstream tests and redistribution license are retained. Additional regression
tests exercise historical bcrypt variants and existing encrypted credentials.
PBKDF2 uses the standard `crypto/pbkdf2` implementation, which the old external
package already wrapped.

`python3 scripts/update_crypto.py --check` reproduces and checks every copied file
against the official Go module checksums and the recorded source/local SHA-256s.
`--check-latest` detects changes to these specific files in the newest release.
The scheduled security workflow checks both, so relevant upstream drift fails CI.
`python3 scripts/check_crypto_advisories.py` also checks the upstream bcrypt and
Blowfish package paths against the official Go vulnerability database.
To update, run `python3 scripts/update_crypto.py --version vMAJOR.MINOR.PATCH`,
review the upstream changes, and pass password/credential compatibility tests and
the full security workflow before merging.

Copied cryptographic packages require explicit upstream monitoring: ordinary Go
module vulnerability matching cannot identify code copied under an internal path.
They must remain exact upstream code rather than becoming a custom algorithm.
