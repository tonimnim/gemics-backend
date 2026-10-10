# Dependency security baseline

Last checked: 2026-08-09.

The production web dependency tree currently reports two high findings. Both
represent the same transitive `image-size` denial-of-service advisories through
Vinext. No compatible patched `image-size` or Vinext release is available in the
current dependency line; npm proposes downgrading Vinext from `1.0.0-beta.2` to
`0.0.45`, which is not a safe automatic fix. The application does not accept
untrusted image parsing on the landing-page route, but the finding remains a
release risk to review when Vinext publishes a compatible update.

The player app is a Flutter project in its own repository; audit its
dependencies there.

Before a production release:

1. Run `npm audit --omit=dev` at the repository root and in `apps/admin`.
2. Review Vinext security releases and update as a unit.
3. Rebuild and smoke-test the Docker images after any dependency or override change.
