/**
 * Shared Multica brand mark used by the native client.
 */
import Svg, { Defs, LinearGradient, Path, Rect, Stop } from "react-native-svg";

interface MulticaLogoProps {
  size?: number;
  /** @deprecated The brand mark now uses its fixed supplied colours. */
  color?: string;
}

export function MulticaLogo({ size = 48 }: MulticaLogoProps) {
  return (
    <Svg width={size} height={size} viewBox="0 0 80 80">
      {/* 2026-09-02 coder(lq): Use the same rounded, layered mark as Web/Electron. */}
      <Defs>
        <LinearGradient id="multica-mobile-background" x1="14" y1="8" x2="66" y2="72" gradientUnits="userSpaceOnUse">
          <Stop offset="0" stopColor="#5c78a2" />
          <Stop offset="1" stopColor="#354b6a" />
        </LinearGradient>
        <LinearGradient id="multica-mobile-mark" x1="40" y1="23" x2="40" y2="56" gradientUnits="userSpaceOnUse">
          <Stop offset="0" stopColor="#fff9e9" />
          <Stop offset="1" stopColor="#f6e8c8" />
        </LinearGradient>
      </Defs>
      <Rect x="2.4" y="2.4" width="75.2" height="75.2" rx="19" fill="url(#multica-mobile-background)" />
      <Rect x="2.6" y="2.6" width="74.8" height="74.8" rx="18.8" fill="none" stroke="#ffffff" strokeOpacity=".16" strokeWidth=".4" />
      <Path
        d="M20.8 55.5V26.7c0-1.7 1.3-3.2 3.1-3.2 1 0 2 .5 2.6 1.3L40 46.4l13.5-21.6c.7-.8 1.6-1.3 2.7-1.3 1.7 0 3.1 1.5 3.1 3.2v28.8"
        fill="none"
        stroke="url(#multica-mobile-mark)"
        strokeWidth="7.2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </Svg>
  );
}
