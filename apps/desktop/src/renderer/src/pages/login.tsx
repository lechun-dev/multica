import { DingTalkFirstLoginFrame, LoginPage } from "@multica/views/auth";
import { DragStrip } from "@multica/views/platform";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { buildDingTalkLoginURL } from "@multica/core/auth";

function requireRuntimeApiUrl(): string {
  const runtimeConfig = window.desktopAPI.runtimeConfig;
  if (!runtimeConfig.ok) {
    throw new Error(
      "Invariant violated: DesktopLoginPage rendered before App accepted runtime config",
    );
  }
  return runtimeConfig.config.apiUrl;
}

export function DesktopLoginPage() {
  const apiUrl = requireRuntimeApiUrl();
  const handleDingTalkLogin = () => {
    const client = import.meta.env.DEV ? "desktop-dev" : "desktop";
    window.desktopAPI.openExternal(buildDingTalkLoginURL(apiUrl, client));
  };

  return (
    <div className="flex h-screen flex-col">
      <DragStrip />
      <DingTalkFirstLoginFrame>
        <LoginPage
          logo={<MulticaIcon bordered size="lg" />}
          hideEmailLogin
          onSuccess={() => {
            // Auth store update triggers AppContent re-render → shows DesktopShell.
            // Initial workspace navigation happens in routes.tsx via IndexRedirect.
          }}
          onDingTalkLogin={handleDingTalkLogin}
        />
      </DingTalkFirstLoginFrame>
    </div>
  );
}
