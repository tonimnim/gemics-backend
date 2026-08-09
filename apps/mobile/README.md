# Gamics mobile

The player product is an Expo SDK 57 / React Native application using Expo
Router. It targets Android and iOS; the marketing site remains the web surface.

Copy `.env.example` to `.env` and replace the sample LAN address with the
machine running the Go API. Android emulators can use `http://10.0.2.2:8080`.

```sh
npm install
npm start
```

Use a development build for product work. Expo Go is useful for early layout
checks, but evidence capture, notifications and secure session storage will be
validated in development builds before release.
