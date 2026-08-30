import { useLocale } from "../../i18n";
import type { DownloadAssets } from "../../utils/parse-release-assets";
import { AppleIcon, WindowsIcon } from "./os-icons";

interface Props {
  assets: DownloadAssets;
}

/**
 * The private deployment release matrix. Keep this limited to the three
 * desktop platform variants that are built and supported for MissionOS.
 */
export function AllPlatforms({
  assets,
}: Props) {
  const { t } = useLocale();
  const d = t.download.allPlatforms;

  return (
    <section
      id="all-platforms"
      className="bg-white py-20 text-[#0a0d12] sm:py-24"
    >
      <div className="mx-auto max-w-[920px] px-4 sm:px-6 lg:px-8">
        <h2 className="landing-serif text-[2.2rem] leading-[1.1] tracking-[-0.03em] sm:text-[2.6rem]">
          {d.title}
        </h2>

        <div className="mt-10 overflow-hidden rounded-2xl border border-[#0a0d12]/10">
          <Row
            icon={<AppleIcon className="text-[#0a0d12]" />}
            label={d.macArm64Label}
            formats={[
              {
                label: d.formatDmg,
                href: assets.macArm64Dmg,
              },
              {
                label: d.formatZip,
                href: assets.macArm64Zip,
              },
            ]}
            unavailable={d.unavailable}
          />
          <Row
            icon={<AppleIcon className="text-[#0a0d12]" />}
            label={d.macX64Label}
            formats={[
              {
                label: d.formatDmg,
                href: assets.macX64Dmg,
              },
              {
                label: d.formatZip,
                href: assets.macX64Zip,
              },
            ]}
            unavailable={d.unavailable}
          />
          <Row
            icon={<WindowsIcon className="text-[#0a0d12]" />}
            label={d.winX64Label}
            formats={[
              {
                label: d.formatExe,
                href: assets.winX64Exe,
              },
            ]}
            unavailable={d.unavailable}
            isLast
          />
        </div>
      </div>
    </section>
  );
}

// ------------------------------------------------------------
// Row
// ------------------------------------------------------------

interface RowProps {
  icon: React.ReactNode;
  label: string;
  formats: {
    label: string;
    href: string | undefined;
  }[];
  unavailable: string;
  isLast?: boolean;
}

function Row({ icon, label, formats, unavailable, isLast }: RowProps) {
  return (
    <div
      className={`flex flex-wrap items-center gap-x-6 gap-y-3 px-6 py-5 ${isLast ? "" : "border-b border-[#0a0d12]/8"}`}
    >
      <div className="flex min-w-[220px] items-center gap-3">
        <span className="flex h-8 w-8 items-center justify-center rounded-lg bg-[#0a0d12]/5">
          {icon}
        </span>
        <span className="text-body font-medium">{label}</span>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        {formats.map((f) =>
          f.href ? (
            <a
              key={f.label}
              href={f.href}
              className="inline-flex items-center gap-1.5 rounded-lg border border-[#0a0d12]/12 bg-white px-3 py-1.5 text-label font-medium transition-colors hover:border-[#0a0d12]/30 hover:bg-[#0a0d12]/5"
            >
              {f.label}
            </a>
          ) : (
            <span
              key={f.label}
              aria-disabled="true"
              className="inline-flex cursor-not-allowed items-center gap-1.5 rounded-lg border border-[#0a0d12]/8 bg-[#0a0d12]/5 px-3 py-1.5 text-label text-[#0a0d12]/40"
              title={unavailable}
            >
              {f.label}
            </span>
          ),
        )}
      </div>
    </div>
  );
}
