// Shared views use the ES2023 immutable sorting API. Some mobile in-app
// browsers still run WebViews without it, so install the spec-compliant shim
// before any application modules render.
import "core-js/actual/array/to-sorted";
