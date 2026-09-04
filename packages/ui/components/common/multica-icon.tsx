import { useState, useEffect } from "react";
import { cn } from "../../lib/utils";

interface MulticaIconProps extends React.ComponentProps<"span"> {
  /**
   * If true, play a one-time entrance spin animation.
   */
  animate?: boolean;
  /**
   * If true, disable hover spin animation.
   */
  noSpin?: boolean;
  /**
   * If true, show a border around the icon.
   */
  bordered?: boolean;
  /**
   * Size of the bordered icon: "sm" (default), "md", "lg"
   */
  size?: "sm" | "md" | "lg";
}

const borderedSizes = {
  sm: { wrapper: "p-1.5", icon: "size-3.5" },
  md: { wrapper: "p-2", icon: "size-4" },
  lg: { wrapper: "p-2.5", icon: "size-5" },
};

/**
 * Shared Multica brand mark.
 *
 * 2026-09-02 coder(lq): Replaced the legacy asterisk with the supplied blue
 * background and rounded cream M mark. Keeping the component/API stable lets
 * Web and Electron adopt the brand without touching their call sites.
 */
export function MulticaIcon({
  className,
  animate = false,
  noSpin = false,
  bordered = false,
  size = "sm",
  ...props
}: MulticaIconProps) {
  const [entranceDone, setEntranceDone] = useState(!animate);

  useEffect(() => {
    if (!animate) return;
    const timer = setTimeout(() => setEntranceDone(true), 600);
    return () => clearTimeout(timer);
  }, [animate]);

  if (bordered) {
    const sizeConfig = borderedSizes[size];
    return (
      <span
        className={cn(
          "inline-flex items-center justify-center border border-border rounded-md",
          sizeConfig.wrapper,
          className
        )}
        aria-hidden="true"
        {...props}
      >
        <span
          className={cn(
            "block",
            sizeConfig.icon,
            !entranceDone && "animate-entrance-spin",
            entranceDone && !noSpin && "hover:animate-spin"
          )}
        >
          <BrandMark />
        </span>
      </span>
    );
  }

  return (
    <span
      className={cn(
        "inline-block size-[1em]",
        !entranceDone && "animate-entrance-spin",
        entranceDone && !noSpin && "hover:animate-spin",
        className
      )}
      aria-hidden="true"
      {...props}
    >
      <BrandMark />
    </span>
  );
}

/** The SVG is intentionally inline so the same mark renders in Next.js and Electron. */
function BrandMark() {
  return (
    <svg
      className="block size-full"
      viewBox="0 0 600 600"
      role="img"
      aria-label="Multica"
      focusable="false"
    >
      <defs>
        <linearGradient id="multica-icon-background" x1="104" y1="64" x2="496" y2="536" gradientUnits="userSpaceOnUse">
          <stop stopColor="#5c78a2" />
          <stop offset="1" stopColor="#354b6a" />
        </linearGradient>
        <linearGradient id="multica-icon-mark" x1="300" y1="176" x2="300" y2="416" gradientUnits="userSpaceOnUse">
          <stop stopColor="#fff9e9" />
          <stop offset="1" stopColor="#f6e8c8" />
        </linearGradient>
      </defs>
      <rect x="18" y="18" width="564" height="564" rx="148" fill="url(#multica-icon-background)" />
      <rect x="19.5" y="19.5" width="561" height="561" rx="146.5" fill="none" stroke="#ffffff" strokeOpacity=".16" strokeWidth="3" />
      <path
        d="M156 416V200c0-13 10-24 23-24 8 0 15 4 20 10l101 172 101-172c5-6 12-10 20-10 13 0 23 11 23 24v216"
        fill="none"
        stroke="url(#multica-icon-mark)"
        strokeWidth="54"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
