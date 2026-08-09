# Dependency security baseline

Last checked: 2026-08-09.

The production web dependency tree currently reports two high findings. Both
represent the same transitive `image-size` denial-of-service advisories through
Vinext. No compatible patched `image-size` or Vinext release is available in the
current dependency line; npm proposes downgrading Vinext from `1.0.0-beta.2` to
`0.0.45`, which is not a safe automatic fix. The application does not accept
untrusted image parsing on the landing-page route, but the finding remains a
release risk to review when Vinext publishes a compatible update.

The Expo SDK 57 mobile dependency tree currently produces 22
`npm audit --omit=dev` findings: 14 high and 8 moderate, with no critical
findings. They are transitive findings in Expo, React Native, Metro and related
build tooling.

`npm audit fix --force` must not be used. npm currently proposes downgrading the
app to Expo SDK 53 and React Native 0.72, which conflicts with the generated SDK
57 dependency set. Expo Doctor passes all 20 compatibility checks on the locked
versions. Recheck advisories and upgrade within the supported Expo SDK line as
patched packages become available.

Before a production release:

1. Run `npm audit --omit=dev` at the repository root and in `apps/mobile`.
2. Run `npx expo-doctor@latest` and `npx expo install --check`.
3. Review Vinext, Expo and React Native security releases and update as a unit.
4. Rebuild and smoke-test both Docker images after any dependency or override change.
