/**
 * Shared Multica brand mark used by the native client.
 */
import Svg, { Path, Rect } from "react-native-svg";

interface MulticaLogoProps {
  size?: number;
  /** @deprecated The brand mark now uses its fixed supplied colours. */
  color?: string;
}

export function MulticaLogo({ size = 48 }: MulticaLogoProps) {
  return (
    <Svg width={size} height={size} viewBox="0 0 80 80">
      {/* 2026-09-02 coder(lq): Match the supplied blue/cream M brand mark. */}
      <Rect width="80" height="80" fill="#496286" />
      <Path
        d="M20.8 55.5V26.7c0-1.7 1.3-3.2 3.1-3.2 1 0 2 .5 2.6 1.3L40 46.4l13.5-21.6c.7-.8 1.6-1.3 2.7-1.3 1.7 0 3.1 1.5 3.1 3.2v28.8"
        fill="none"
        stroke="#fdf4e0"
        strokeWidth="6.9"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </Svg>
  );
}
