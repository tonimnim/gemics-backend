# Dependency security baseline

Last checked: 2026-08-09.

The marketing website has no known production dependency finding recorded in
this scaffold. The Expo SDK 57 mobile dependency tree currently produces 22
`npm audit --omit=dev` findings: 14 high and 8 moderate, with no critical
findings. They are transitive findings in Expo, React Native, Metro and related
build tooling.

`npm audit fix --force` must not be used. npm currently proposes downgrading the
app to Expo SDK 53 and React Native 0.72, which conflicts with the generated SDK
57 dependency set. Expo Doctor passes all 20 compatibility checks on the locked
versions. Recheck advisories and upgrade within the supported Expo SDK line as
patched packages become available.

Before a production release:

1. Run `npm audit --omit=dev` in `apps/mobile`.
2. Run `npx expo-doctor@latest` and `npx expo install --check`.
3. Review Expo and React Native security releases; upgrade the SDK as a unit.
4. Produce a fresh native build after any dependency or override change.
