import { useT } from "./use-t";

// Localized relative-time formatter. Returns a function so call-site usage
// stays terse: `const timeAgo = useTimeAgo(); ...timeAgo(dateStr)`.
export function useTimeAgo() {
  const { t } = useT("common");
  return (dateStr: string): string => {
    const timestamp = new Date(dateStr).getTime();
    // 2026-09-07 coder(lq): Invalid timestamps can arrive in degraded cached
    // data. Hide the relative-time label instead of rendering "NaN days ago".
    if (!Number.isFinite(timestamp)) return "";
    const diff = Date.now() - timestamp;
    const minutes = Math.floor(diff / 60000);
    if (minutes < 1) return t(($) => $.time.just_now);
    if (minutes < 60) return t(($) => $.time.minutes_ago, { count: minutes });
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return t(($) => $.time.hours_ago, { count: hours });
    const days = Math.floor(hours / 24);
    return t(($) => $.time.days_ago, { count: days });
  };
}
